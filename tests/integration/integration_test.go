package integration_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/avalanchego/database/manager"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/snow/consensus/snowman"
	"github.com/ava-labs/avalanchego/snow/engine/common"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/set"
	avago_version "github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/fatih/color"
	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/consts"
	"github.com/ava-labs/hypersdk/crypto"
	hutils "github.com/ava-labs/hypersdk/utils"
	"github.com/ava-labs/hypersdk/vm"

	"github.com/rafael-abuawad/samplevm/actions"
	"github.com/rafael-abuawad/samplevm/auth"
	"github.com/rafael-abuawad/samplevm/client"
	tconsts "github.com/rafael-abuawad/samplevm/consts"
	"github.com/rafael-abuawad/samplevm/controller"
	"github.com/rafael-abuawad/samplevm/genesis"
	"github.com/rafael-abuawad/samplevm/utils"
)

const transferTxFee = 400 /* base fee */ + 72 /* transfer fee */

var (
	logFactory logging.Factory
	log        logging.Logger
)

func init() {
	logFactory = logging.NewFactory(logging.Config{
		DisplayLevel: logging.Debug,
	})
	l, err := logFactory.Make("main")
	if err != nil {
		panic(err)
	}
	log = l
}

func TestIntegration(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "tokenvm integration test suites")
}

var (
	requestTimeout time.Duration
	vms            int
	minPrice       int64
)

func init() {
	flag.DurationVar(
		&requestTimeout,
		"request-timeout",
		120*time.Second,
		"timeout for transaction issuance and confirmation",
	)
	flag.IntVar(
		&vms,
		"vms",
		3,
		"number of VMs to create",
	)
	flag.Int64Var(
		&minPrice,
		"min-price",
		-1,
		"minimum price",
	)
}

var (
	priv    crypto.PrivateKey
	factory *auth.ED25519Factory
	rsender crypto.PublicKey
	sender  string

	priv2    crypto.PrivateKey
	factory2 *auth.ED25519Factory
	rsender2 crypto.PublicKey
	sender2  string

	asset1   []byte
	asset1ID ids.ID
	asset2   []byte
	asset2ID ids.ID
	asset3   []byte
	asset3ID ids.ID

	// when used with embedded VMs
	genesisBytes []byte
	instances    []instance

	gen *genesis.Genesis
)

type instance struct {
	chainID    ids.ID
	nodeID     ids.NodeID
	vm         *vm.VM
	toEngine   chan common.Message
	httpServer *httptest.Server
	cli        *client.Client // clients for embedded VMs
}

var _ = ginkgo.BeforeSuite(func() {
	gomega.Ω(vms).Should(gomega.BeNumerically(">", 1))

	var err error
	priv, err = crypto.GeneratePrivateKey()
	gomega.Ω(err).Should(gomega.BeNil())
	factory = auth.NewED25519Factory(priv)
	rsender = priv.PublicKey()
	sender = utils.Address(rsender)
	log.Debug(
		"generated key",
		zap.String("addr", sender),
		zap.String("pk", hex.EncodeToString(priv[:])),
	)

	priv2, err = crypto.GeneratePrivateKey()
	gomega.Ω(err).Should(gomega.BeNil())
	factory2 = auth.NewED25519Factory(priv2)
	rsender2 = priv2.PublicKey()
	sender2 = utils.Address(rsender2)
	log.Debug(
		"generated key",
		zap.String("addr", sender2),
		zap.String("pk", hex.EncodeToString(priv2[:])),
	)

	asset1 = []byte("1")
	asset2 = []byte("2")
	asset3 = []byte("3")

	// create embedded VMs
	instances = make([]instance, vms)

	gen = genesis.Default()
	if minPrice >= 0 {
		gen.MinUnitPrice = uint64(minPrice)
	}
	gen.WindowTargetBlocks = 1_000_000 // deactivate block fee
	gen.CustomAllocation = []*genesis.CustomAllocation{
		{
			Address: sender,
			Balance: 10_000_000,
		},
	}
	genesisBytes, err = json.Marshal(gen)
	gomega.Ω(err).Should(gomega.BeNil())

	networkID := uint32(1)
	subnetID := ids.GenerateTestID()
	chainID := ids.GenerateTestID()

	app := &appSender{}
	for i := range instances {
		nodeID := ids.GenerateTestNodeID()
		sk, err := bls.NewSecretKey()
		gomega.Ω(err).Should(gomega.BeNil())
		l, err := logFactory.Make(nodeID.String())
		gomega.Ω(err).Should(gomega.BeNil())
		dname, err := os.MkdirTemp("", fmt.Sprintf("%s-chainData", nodeID.String()))
		gomega.Ω(err).Should(gomega.BeNil())
		snowCtx := &snow.Context{
			NetworkID:      networkID,
			SubnetID:       subnetID,
			ChainID:        chainID,
			NodeID:         nodeID,
			Log:            l,
			ChainDataDir:   dname,
			Metrics:        metrics.NewOptionalGatherer(),
			PublicKey:      bls.PublicFromSecretKey(sk),
			WarpSigner:     warp.NewSigner(sk, chainID),
			ValidatorState: &validators.TestState{},
		}

		toEngine := make(chan common.Message, 1)
		db := manager.NewMemDB(avago_version.CurrentDatabase)

		v := controller.New()
		err = v.Initialize(
			context.TODO(),
			snowCtx,
			db,
			genesisBytes,
			nil,
			[]byte(
				`{"parallelism":3, "testMode":true, "logLevel":"debug", "trackedPairs":["*"]}`,
			),
			toEngine,
			nil,
			app,
		)
		gomega.Ω(err).Should(gomega.BeNil())

		var hd map[string]*common.HTTPHandler
		hd, err = v.CreateHandlers(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())

		httpServer := httptest.NewServer(hd[vm.Endpoint].Handler)
		instances[i] = instance{
			chainID:    snowCtx.ChainID,
			nodeID:     snowCtx.NodeID,
			vm:         v,
			toEngine:   toEngine,
			httpServer: httpServer,
			cli:        client.New(httpServer.URL),
		}

		// Force sync ready (to mimic bootstrapping from genesis)
		v.ForceReady()
	}

	// Verify genesis allocations loaded correctly (do here otherwise test may
	// check during and it will be inaccurate)
	for _, inst := range instances {
		cli := inst.cli
		g, err := cli.Genesis(context.Background())
		gomega.Ω(err).Should(gomega.BeNil())

		csupply := uint64(0)
		for _, alloc := range g.CustomAllocation {
			balance, err := cli.Balance(context.Background(), alloc.Address, ids.Empty)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(balance).Should(gomega.Equal(alloc.Balance))
			csupply += alloc.Balance
		}
		exists, metadata, supply, owner, warp, err := cli.Asset(context.Background(), ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(string(metadata)).Should(gomega.Equal(tconsts.Symbol))
		gomega.Ω(supply).Should(gomega.Equal(csupply))
		gomega.Ω(owner).Should(gomega.Equal(utils.Address(crypto.EmptyPublicKey)))
		gomega.Ω(warp).Should(gomega.BeFalse())
	}

	app.instances = instances
	color.Blue("created %d VMs", vms)
})

var _ = ginkgo.AfterSuite(func() {
	for _, iv := range instances {
		iv.httpServer.Close()
		err := iv.vm.Shutdown(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
	}
})

var _ = ginkgo.Describe("[Ping]", func() {
	ginkgo.It("can ping", func() {
		for _, inst := range instances {
			cli := inst.cli
			ok, err := cli.Ping(context.Background())
			gomega.Ω(ok).Should(gomega.BeTrue())
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})
})

var _ = ginkgo.Describe("[Network]", func() {
	ginkgo.It("can get network", func() {
		for _, inst := range instances {
			cli := inst.cli
			networkID, subnetID, chainID, err := cli.Network(context.Background())
			gomega.Ω(networkID).Should(gomega.Equal(uint32(1)))
			gomega.Ω(subnetID).ShouldNot(gomega.Equal(ids.Empty))
			gomega.Ω(chainID).ShouldNot(gomega.Equal(ids.Empty))
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})
})

var _ = ginkgo.Describe("[Tx Processing]", func() {
	ginkgo.It("get currently accepted block ID", func() {
		for _, inst := range instances {
			cli := inst.cli
			_, _, _, err := cli.Accepted(context.Background())
			gomega.Ω(err).Should(gomega.BeNil())
		}
	})

	var transferTxRoot *chain.Transaction
	ginkgo.It("Gossip TransferTx to a different node", func() {
		ginkgo.By("issue TransferTx", func() {
			submit, transferTx, _, err := instances[0].cli.GenerateTransaction(
				context.Background(),
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 100_000, // must be more than StateLockup
				},
				factory,
			)
			transferTxRoot = transferTx
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			gomega.Ω(instances[0].vm.Mempool().Len(context.Background())).Should(gomega.Equal(1))
		})

		ginkgo.By("skip duplicate", func() {
			_, err := instances[0].cli.SubmitTx(
				context.Background(),
				transferTxRoot.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("send gossip from node 0 to 1", func() {
			err := instances[0].vm.Gossiper().TriggerGossip(context.TODO())
			gomega.Ω(err).Should(gomega.BeNil())
		})

		ginkgo.By("skip invalid time", func() {
			actionRegistry, authRegistry := instances[0].vm.Registry()
			tx := chain.NewTx(
				&chain.Base{
					ChainID:   instances[0].chainID,
					Timestamp: 0,
					UnitPrice: 1000,
				},
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 110,
				},
			)
			// Must do manual construction to avoid `tx.Sign` error (would fail with
			// 0 timestamp)
			msg, err := tx.Digest(actionRegistry)
			gomega.Ω(err).To(gomega.BeNil())
			auth, err := factory.Sign(msg, tx.Action)
			gomega.Ω(err).To(gomega.BeNil())
			tx.Auth = auth
			p := codec.NewWriter(consts.MaxInt)
			gomega.Ω(tx.Marshal(p, actionRegistry, authRegistry)).To(gomega.BeNil())
			gomega.Ω(p.Err()).To(gomega.BeNil())
			_, err = instances[0].cli.SubmitTx(
				context.Background(),
				p.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("skip duplicate (after gossip, which shouldn't clear)", func() {
			_, err := instances[0].cli.SubmitTx(
				context.Background(),
				transferTxRoot.Bytes(),
			)
			gomega.Ω(err).To(gomega.Not(gomega.BeNil()))
		})

		ginkgo.By("receive gossip in the node 1, and signal block build", func() {
			instances[1].vm.Builder().TriggerBuild()
			<-instances[1].toEngine
		})

		ginkgo.By("build block in the node 1", func() {
			ctx := context.TODO()
			blk, err := instances[1].vm.BuildBlock(ctx)
			gomega.Ω(err).To(gomega.BeNil())

			gomega.Ω(blk.Verify(ctx)).To(gomega.BeNil())
			gomega.Ω(blk.Status()).To(gomega.Equal(choices.Processing))

			err = instances[1].vm.SetPreference(ctx, blk.ID())
			gomega.Ω(err).To(gomega.BeNil())

			gomega.Ω(blk.Accept(ctx)).To(gomega.BeNil())
			gomega.Ω(blk.Status()).To(gomega.Equal(choices.Accepted))

			lastAccepted, err := instances[1].vm.LastAccepted(ctx)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(lastAccepted).To(gomega.Equal(blk.ID()))

			results := blk.(*chain.StatelessBlock).Results()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
			gomega.Ω(results[0].Units).Should(gomega.Equal(uint64(transferTxFee)))
			gomega.Ω(results[0].Output).Should(gomega.BeNil())
		})

		ginkgo.By("ensure balance is updated", func() {
			balance, err := instances[1].cli.Balance(context.Background(), sender, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance).To(gomega.Equal(uint64(9899528)))
			balance2, err := instances[1].cli.Balance(context.Background(), sender2, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance2).To(gomega.Equal(uint64(100000)))
		})
	})

	ginkgo.It("ensure multiple txs work ", func() {
		ginkgo.By("transfer funds again", func() {
			submit, _, _, err := instances[1].cli.GenerateTransaction(
				context.Background(),
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 101,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			accept := expectBlk(instances[1])
			results := accept()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())

			balance2, err := instances[1].cli.Balance(context.Background(), sender2, ids.Empty)
			gomega.Ω(err).To(gomega.BeNil())
			gomega.Ω(balance2).To(gomega.Equal(uint64(100101)))
		})
	})

	ginkgo.It("Test processing block handling", func() {
		var accept, accept2 func() []*chain.Result

		ginkgo.By("create processing tip", func() {
			submit, _, _, err := instances[1].cli.GenerateTransaction(
				context.Background(),
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 200,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			accept = expectBlk(instances[1])

			submit, _, _, err = instances[1].cli.GenerateTransaction(
				context.Background(),
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 201,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
			accept2 = expectBlk(instances[1])
		})

		ginkgo.By("clear processing tip", func() {
			results := accept()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
			results = accept2()
			gomega.Ω(results).Should(gomega.HaveLen(1))
			gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		})
	})

	ginkgo.It("ensure mempool works", func() {
		ginkgo.By("fail Gossip TransferTx to a stale node when missing previous blocks", func() {
			submit, _, _, err := instances[1].cli.GenerateTransaction(
				context.Background(),
				nil,
				&actions.Transfer{
					To:    rsender2,
					Value: 203,
				},
				factory,
			)
			gomega.Ω(err).Should(gomega.BeNil())
			gomega.Ω(submit(context.Background())).Should(gomega.BeNil())

			err = instances[1].vm.Gossiper().TriggerGossip(context.TODO())
			gomega.Ω(err).Should(gomega.BeNil())

			// mempool in 0 should be 1 (old amount), since gossip/submit failed
			gomega.Ω(instances[0].vm.Mempool().Len(context.TODO())).Should(gomega.Equal(1))
		})
	})

	ginkgo.It("ensure unprocessed tip works", func() {
		ginkgo.By("import accepted blocks to instance 2", func() {
			ctx := context.TODO()
			o := instances[1]
			blks := []snowman.Block{}
			next, err := o.vm.LastAccepted(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			for {
				blk, err := o.vm.GetBlock(ctx, next)
				gomega.Ω(err).Should(gomega.BeNil())
				blks = append([]snowman.Block{blk}, blks...)
				if blk.Height() == 1 {
					break
				}
				next = blk.Parent()
			}

			n := instances[2]
			blk1, err := n.vm.ParseBlock(ctx, blks[0].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk1.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Parse tip
			blk2, err := n.vm.ParseBlock(ctx, blks[1].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			blk3, err := n.vm.ParseBlock(ctx, blks[2].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())

			// Verify tip
			err = blk2.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk3.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Accept tip
			err = blk1.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk2.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk3.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())

			// Parse another
			blk4, err := n.vm.ParseBlock(ctx, blks[3].Bytes())
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk4.Verify(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
			err = blk4.Accept(ctx)
			gomega.Ω(err).Should(gomega.BeNil())
		})
	})

	ginkgo.It("processes valid index transactions (w/block listening)", func() {
		// Clear previous txs on instance 0
		accept := expectBlk(instances[0])
		accept() // don't care about results

		// Subscribe to blocks
		blocksPort, err := instances[0].cli.BlocksPort(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(blocksPort).Should(gomega.Not(gomega.Equal(0)))
		tcpURI := fmt.Sprintf("127.0.0.1:%d", blocksPort)
		cli, err := vm.NewBlockRPCClient(tcpURI)
		gomega.Ω(err).Should(gomega.BeNil())

		// Fetch balances
		balance, err := instances[0].cli.Balance(context.TODO(), sender, ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())

		// Send tx
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		transfer := &actions.Transfer{
			To:    other.PublicKey(),
			Value: 1,
		}

		submit, rawTx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			transfer,
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())

		gomega.Ω(err).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		// Read item from connection
		blk, lresults, err := cli.Listen(instances[0].vm)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(len(blk.Txs)).Should(gomega.Equal(1))
		tx := blk.Txs[0].Action.(*actions.Transfer)
		gomega.Ω(tx.Asset).To(gomega.Equal(ids.Empty))
		gomega.Ω(tx.Value).To(gomega.Equal(uint64(1)))
		gomega.Ω(lresults).Should(gomega.Equal(results))
		gomega.Ω(cli.Close()).Should(gomega.BeNil())

		// Check balance modifications are correct
		balancea, err := instances[0].cli.Balance(context.TODO(), sender, ids.Empty)
		gomega.Ω(err).Should(gomega.BeNil())
		g, err := instances[0].cli.Genesis(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
		r := g.Rules(time.Now().Unix())
		maxUnits, err := rawTx.MaxUnits(r)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(balancea + maxUnits + 1))
	})

	ginkgo.It("processes valid index transactions (w/streaming verification)", func() {
		// Create streaming client
		decisionsPort, err := instances[0].cli.DecisionsPort(context.TODO())
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(decisionsPort).Should(gomega.Not(gomega.Equal(0)))
		tcpURI := fmt.Sprintf("127.0.0.1:%d", decisionsPort)
		cli, err := vm.NewDecisionRPCClient(tcpURI)
		gomega.Ω(err).Should(gomega.BeNil())

		// Create tx
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		transfer := &actions.Transfer{
			To:    other.PublicKey(),
			Value: 1,
		}
		_, tx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			transfer,
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())

		// Submit tx and accept block
		gomega.Ω(cli.IssueTx(tx)).Should(gomega.BeNil())
		for instances[0].vm.Mempool().Len(context.TODO()) == 0 {
			// We need to wait for mempool to be populated because issuance will
			// return as soon as bytes are on the channel.
			hutils.Outf("{{yellow}}waiting for mempool to return non-zero txs{{/}}\n")
			time.Sleep(500 * time.Millisecond)
		}
		gomega.Ω(err).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		// Read decision from connection
		txID, dErr, result, err := cli.Listen()
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(txID).Should(gomega.Equal(tx.ID()))
		gomega.Ω(dErr).Should(gomega.BeNil())
		gomega.Ω(result.Success).Should(gomega.BeTrue())
		gomega.Ω(result).Should(gomega.Equal(results[0]))
		gomega.Ω(cli.Close()).Should(gomega.BeNil())
	})

	ginkgo.It("mint an asset that doesn't exist", func() {
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		assetID := ids.GenerateTestID()
		submit, _, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: assetID,
				Value: 10,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("asset missing"))

		exists, _, _, _, _, err := instances[0].cli.Asset(context.TODO(), assetID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeFalse())
	})

	ginkgo.It("create a new asset (no metadata)", func() {
		submit, tx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.CreateAsset{
				Metadata: nil,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		assetID := tx.ID()
		balance, err := instances[0].cli.Balance(context.TODO(), sender, assetID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))
		exists, metadata, supply, owner, warp, err := instances[0].cli.Asset(
			context.TODO(),
			assetID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.HaveLen(0))
		gomega.Ω(supply).Should(gomega.Equal(uint64(0)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("create asset with too long of metadata", func() {
		actionRegistry, authRegistry := instances[0].vm.Registry()
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].chainID,
				Timestamp: time.Now().Unix(),
				UnitPrice: 1000,
			},
			nil,
			&actions.CreateAsset{
				Metadata: make([]byte, actions.MaxMetadataSize*2),
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// too large)
		msg, err := tx.Digest(actionRegistry)
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(consts.MaxInt)
		gomega.Ω(tx.Marshal(p, actionRegistry, authRegistry)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("size is larger than limit"))
	})

	ginkgo.It("create a new asset (simple metadata)", func() {
		submit, tx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.CreateAsset{
				Metadata: asset1,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		asset1ID = tx.ID()
		balance, err := instances[0].cli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].cli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(0)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("mint a new asset", func() {
		submit, _, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset1ID,
				Value: 15,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].cli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(15)))
		balance, err = instances[0].cli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].cli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(15)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("mint asset from wrong owner", func() {
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		submit, _, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: asset1ID,
				Value: 10,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("wrong owner"))

		exists, metadata, supply, owner, warp, err := instances[0].cli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(15)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("rejects empty mint", func() {
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		actionRegistry, authRegistry := instances[0].vm.Registry()
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].chainID,
				Timestamp: time.Now().Unix(),
				UnitPrice: 1000,
			},
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Asset: asset1ID,
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// bad codec)
		msg, err := tx.Digest(actionRegistry)
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(consts.MaxInt)
		gomega.Ω(tx.Marshal(p, actionRegistry, authRegistry)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("Uint64 field is not populated"))
	})

	ginkgo.It("reject max mint", func() {
		submit, _, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset1ID,
				Value: consts.MaxUint64,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		result := results[0]
		gomega.Ω(result.Success).Should(gomega.BeFalse())
		gomega.Ω(string(result.Output)).
			Should(gomega.ContainSubstring("overflow"))

		balance, err := instances[0].cli.Balance(context.TODO(), sender2, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
		balance, err = instances[0].cli.Balance(context.TODO(), sender, asset1ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(0)))

		exists, metadata, supply, owner, warp, err := instances[0].cli.Asset(
			context.TODO(),
			asset1ID,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(exists).Should(gomega.BeTrue())
		gomega.Ω(metadata).Should(gomega.Equal(asset1))
		gomega.Ω(supply).Should(gomega.Equal(uint64(10)))
		gomega.Ω(owner).Should(gomega.Equal(sender))
		gomega.Ω(warp).Should(gomega.BeFalse())
	})

	ginkgo.It("rejects mint of native token", func() {
		other, err := crypto.GeneratePrivateKey()
		gomega.Ω(err).Should(gomega.BeNil())
		actionRegistry, authRegistry := instances[0].vm.Registry()
		tx := chain.NewTx(
			&chain.Base{
				ChainID:   instances[0].chainID,
				Timestamp: time.Now().Unix(),
				UnitPrice: 1000,
			},
			nil,
			&actions.MintAsset{
				To:    other.PublicKey(),
				Value: 10,
			},
		)
		// Must do manual construction to avoid `tx.Sign` error (would fail with
		// bad codec)
		msg, err := tx.Digest(actionRegistry)
		gomega.Ω(err).To(gomega.BeNil())
		auth, err := factory.Sign(msg, tx.Action)
		gomega.Ω(err).To(gomega.BeNil())
		tx.Auth = auth
		p := codec.NewWriter(consts.MaxInt)
		gomega.Ω(tx.Marshal(p, actionRegistry, authRegistry)).To(gomega.BeNil())
		gomega.Ω(p.Err()).To(gomega.BeNil())
		_, err = instances[0].cli.SubmitTx(
			context.Background(),
			p.Bytes(),
		)
		gomega.Ω(err.Error()).Should(gomega.ContainSubstring("ID field is not populated"))
	})

	ginkgo.It("mints another new asset (to self)", func() {
		submit, tx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.CreateAsset{
				Metadata: asset2,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		asset2ID = tx.ID()

		submit, _, _, err = instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    rsender,
				Asset: asset2ID,
				Value: 10,
			},
			factory,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results = accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].cli.Balance(context.TODO(), sender, asset2ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
	})

	ginkgo.It("mints another new asset (to self) on another account", func() {
		submit, tx, _, err := instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.CreateAsset{
				Metadata: asset3,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept := expectBlk(instances[0])
		results := accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())
		asset3ID = tx.ID()

		submit, _, _, err = instances[0].cli.GenerateTransaction(
			context.Background(),
			nil,
			&actions.MintAsset{
				To:    rsender2,
				Asset: asset3ID,
				Value: 10,
			},
			factory2,
		)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(submit(context.Background())).Should(gomega.BeNil())
		accept = expectBlk(instances[0])
		results = accept()
		gomega.Ω(results).Should(gomega.HaveLen(1))
		gomega.Ω(results[0].Success).Should(gomega.BeTrue())

		balance, err := instances[0].cli.Balance(context.TODO(), sender2, asset3ID)
		gomega.Ω(err).Should(gomega.BeNil())
		gomega.Ω(balance).Should(gomega.Equal(uint64(10)))
	})
})

func expectBlk(i instance) func() []*chain.Result {
	ctx := context.TODO()

	// manually signal ready
	i.vm.Builder().TriggerBuild()
	// manually ack ready sig as in engine
	<-i.toEngine

	blk, err := i.vm.BuildBlock(ctx)
	gomega.Ω(err).To(gomega.BeNil())
	gomega.Ω(blk).To(gomega.Not(gomega.BeNil()))

	gomega.Ω(blk.Verify(ctx)).To(gomega.BeNil())
	gomega.Ω(blk.Status()).To(gomega.Equal(choices.Processing))

	err = i.vm.SetPreference(ctx, blk.ID())
	gomega.Ω(err).To(gomega.BeNil())

	return func() []*chain.Result {
		gomega.Ω(blk.Accept(ctx)).To(gomega.BeNil())
		gomega.Ω(blk.Status()).To(gomega.Equal(choices.Accepted))

		lastAccepted, err := i.vm.LastAccepted(ctx)
		gomega.Ω(err).To(gomega.BeNil())
		gomega.Ω(lastAccepted).To(gomega.Equal(blk.ID()))
		return blk.(*chain.StatelessBlock).Results()
	}
}

var _ common.AppSender = &appSender{}

type appSender struct {
	next      int
	instances []instance
}

func (app *appSender) SendAppGossip(ctx context.Context, appGossipBytes []byte) error {
	n := len(app.instances)
	sender := app.instances[app.next].nodeID
	app.next++
	app.next %= n
	return app.instances[app.next].vm.AppGossip(ctx, sender, appGossipBytes)
}

func (*appSender) SendAppRequest(context.Context, set.Set[ids.NodeID], uint32, []byte) error {
	return nil
}

func (*appSender) SendAppResponse(context.Context, ids.NodeID, uint32, []byte) error {
	return nil
}

func (*appSender) SendAppGossipSpecific(context.Context, set.Set[ids.NodeID], []byte) error {
	return nil
}

func (*appSender) SendCrossChainAppRequest(context.Context, ids.ID, uint32, []byte) error {
	return nil
}

func (*appSender) SendCrossChainAppResponse(context.Context, ids.ID, uint32, []byte) error {
	return nil
}
