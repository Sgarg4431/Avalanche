//nolint:lll
package cmd

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	runner "github.com/ava-labs/avalanche-network-runner/client"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/hypersdk/utils"
	"github.com/ava-labs/hypersdk/vm"
	"github.com/spf13/cobra"

	"github.com/rafael-abuawad/samplevm/actions"
	"github.com/rafael-abuawad/samplevm/auth"
	"github.com/rafael-abuawad/samplevm/client"
	"github.com/rafael-abuawad/samplevm/consts"
	tutils "github.com/rafael-abuawad/samplevm/utils"
)

var chainCmd = &cobra.Command{
	Use: "chain",
	RunE: func(*cobra.Command, []string) error {
		return ErrMissingSubcommand
	},
}

var importChainCmd = &cobra.Command{
	Use: "import",
	RunE: func(_ *cobra.Command, args []string) error {
		chainID, err := promptID("chainID")
		if err != nil {
			return err
		}
		uri, err := promptString("uri")
		if err != nil {
			return err
		}
		if err := StoreChain(chainID, uri); err != nil {
			return err
		}
		if err := StoreDefault(defaultChainKey, chainID[:]); err != nil {
			return err
		}
		return nil
	},
}

var importANRChainCmd = &cobra.Command{
	Use: "import-anr",
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()

		// Delete previous items
		oldChains, err := DeleteChains()
		if err != nil {
			return err
		}
		if len(oldChains) > 0 {
			utils.Outf("{{yellow}}deleted old chains:{{/}} %+v\n", oldChains)
		}

		// Load new items from ANR
		anrCli, err := runner.New(runner.Config{
			Endpoint:    "0.0.0.0:12352",
			DialTimeout: 10 * time.Second,
		}, logging.NoLog{})
		if err != nil {
			return err
		}
		status, err := anrCli.Status(ctx)
		if err != nil {
			return err
		}
		subnets := map[ids.ID][]ids.ID{}
		for chain, chainInfo := range status.ClusterInfo.CustomChains {
			chainID, err := ids.FromString(chain)
			if err != nil {
				return err
			}
			subnetID, err := ids.FromString(chainInfo.SubnetId)
			if err != nil {
				return err
			}
			chainIDs, ok := subnets[subnetID]
			if !ok {
				chainIDs = []ids.ID{}
			}
			chainIDs = append(chainIDs, chainID)
			subnets[subnetID] = chainIDs
		}
		var filledChainID ids.ID
		for _, nodeInfo := range status.ClusterInfo.NodeInfos {
			if len(nodeInfo.WhitelistedSubnets) == 0 {
				continue
			}
			trackedSubnets := strings.Split(nodeInfo.WhitelistedSubnets, ",")
			for _, subnet := range trackedSubnets {
				subnetID, err := ids.FromString(subnet)
				if err != nil {
					return err
				}
				for _, chainID := range subnets[subnetID] {
					uri := fmt.Sprintf("%s/ext/bc/%s", nodeInfo.Uri, chainID)
					if err := StoreChain(chainID, uri); err != nil {
						return err
					}
					utils.Outf(
						"{{yellow}}stored chainID:{{/}} %s {{yellow}}uri:{{/}} %s\n",
						chainID,
						uri,
					)
					filledChainID = chainID
				}
			}
		}
		return StoreDefault(defaultChainKey, filledChainID[:])
	},
}

var setChainCmd = &cobra.Command{
	Use: "set",
	RunE: func(*cobra.Command, []string) error {
		chainID, _, err := promptChain("set default chain", nil)
		if err != nil {
			return err
		}
		return StoreDefault(defaultChainKey, chainID[:])
	},
}

var chainInfoCmd = &cobra.Command{
	Use: "info",
	RunE: func(_ *cobra.Command, args []string) error {
		_, uris, err := promptChain("select chainID", nil)
		if err != nil {
			return err
		}
		cli := client.New(uris[0])
		networkID, subnetID, chainID, err := cli.Network(context.Background())
		if err != nil {
			return err
		}
		utils.Outf(
			"{{cyan}}networkID:{{/}} %d {{cyan}}subnetID:{{/}} %s {{cyan}}chainID:{{/}} %s",
			networkID,
			subnetID,
			chainID,
		)
		return nil
	},
}

var watchChainCmd = &cobra.Command{
	Use: "watch",
	RunE: func(_ *cobra.Command, args []string) error {
		ctx := context.Background()
		chainID, uris, err := promptChain("select chainID", nil)
		if err != nil {
			return err
		}
		if err := CloseDatabase(); err != nil {
			return err
		}
		cli := client.New(uris[0])
		port, err := cli.BlocksPort(ctx)
		if err != nil {
			return err
		}
		host, err := utils.GetHost(uris[0])
		if err != nil {
			return err
		}
		scli, err := vm.NewBlockRPCClient(fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			return err
		}
		defer scli.Close()
		parser, err := cli.Parser(ctx)
		if err != nil {
			return err
		}
		totalTxs := float64(0)
		start := time.Now()
		utils.Outf("{{green}}watching for new blocks on %s 👀{{/}}\n", chainID)
		for ctx.Err() == nil {
			blk, results, err := scli.Listen(parser)
			if err != nil {
				return err
			}
			totalTxs += float64(len(blk.Txs))
			utils.Outf(
				"{{green}}height:{{/}}%d {{green}}txs:{{/}}%d {{green}}units:{{/}}%d {{green}}root:{{/}}%s {{green}}avg TPS:{{/}}%f\n", //nolint:lll
				blk.Hght,
				len(blk.Txs),
				blk.UnitsConsumed,
				blk.StateRoot,
				totalTxs/time.Since(start).Seconds(),
			)
			if hideTxs {
				continue
			}
			for i, tx := range blk.Txs {
				result := results[i]
				summaryStr := string(result.Output)
				actor := auth.GetActor(tx.Auth)
				status := "⚠️"
				if result.Success {
					status = "✅"
					switch action := tx.Action.(type) {
					case *actions.CreateAsset:
						summaryStr = fmt.Sprintf("assetID: %s metadata:%s", tx.ID(), string(action.Metadata))

					case *actions.MintAsset:
						amountStr := strconv.FormatUint(action.Value, 10)
						assetStr := action.Asset.String()
						if action.Asset == ids.Empty {
							amountStr = utils.FormatBalance(action.Value)
							assetStr = consts.Symbol
						}
						summaryStr = fmt.Sprintf("%s %s -> %s", amountStr, assetStr, tutils.Address(action.To))

					case *actions.Transfer:
						amountStr := strconv.FormatUint(action.Value, 10)
						assetStr := action.Asset.String()
						if action.Asset == ids.Empty {
							amountStr = utils.FormatBalance(action.Value)
							assetStr = consts.Symbol
						}
						summaryStr = fmt.Sprintf("%s %s -> %s", amountStr, assetStr, tutils.Address(action.To))
					}
				}
				utils.Outf(
					"%s {{yellow}}%s{{/}} {{yellow}}actor:{{/}} %s {{yellow}}units:{{/}} %d {{yellow}}summary (%s):{{/}} [%s]\n",
					status,
					tx.ID(),
					tutils.Address(actor),
					result.Units,
					reflect.TypeOf(tx.Action),
					summaryStr,
				)
			}
		}
		return nil
	},
}
