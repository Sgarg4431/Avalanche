//nolint:lll
package cmd

import (
	"context"
	"errors"
	"time"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/consts"
	"github.com/ava-labs/hypersdk/crypto"
	hutils "github.com/ava-labs/hypersdk/utils"
	"github.com/manifoldco/promptui"
	"github.com/rafael-abuawad/samplevm/actions"
	"github.com/rafael-abuawad/samplevm/client"
	"github.com/rafael-abuawad/samplevm/utils"
	"github.com/spf13/cobra"
)

const (
	dummyBlockAgeThreshold = 25
	dummyHeightThreshold   = 3
)

var actionCmd = &cobra.Command{
	Use: "action",
	RunE: func(*cobra.Command, []string) error {
		return ErrMissingSubcommand
	},
}

var transferCmd = &cobra.Command{
	Use: "transfer",
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()
		_, priv, factory, cli, err := defaultActor()
		if err != nil {
			return err
		}

		// Select token to send
		assetID, err := promptAsset("assetID", true)
		if err != nil {
			return err
		}
		balance, _, err := getAssetInfo(ctx, cli, priv.PublicKey(), assetID, true)
		if balance == 0 || err != nil {
			return err
		}

		// Select recipient
		recipient, err := promptAddress("recipient")
		if err != nil {
			return err
		}

		// Select amount
		amount, err := promptAmount("amount", assetID, balance, nil)
		if err != nil {
			return err
		}

		// Confirm action
		cont, err := promptContinue()
		if !cont || err != nil {
			return err
		}

		// Generate transaction
		submit, tx, _, err := cli.GenerateTransaction(ctx, nil, &actions.Transfer{
			To:    recipient,
			Asset: assetID,
			Value: amount,
		}, factory)
		if err != nil {
			return err
		}
		if err := submit(ctx); err != nil {
			return err
		}
		success, err := cli.WaitForTransaction(ctx, tx.ID())
		if err != nil {
			return err
		}
		printStatus(tx.ID(), success)
		return nil
	},
}

var createAssetCmd = &cobra.Command{
	Use: "create-asset",
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()
		_, _, factory, cli, err := defaultActor()
		if err != nil {
			return err
		}

		// Add metadata to token
		promptText := promptui.Prompt{
			Label: "metadata (can be changed later)",
			Validate: func(input string) error {
				if len(input) > actions.MaxMetadataSize {
					return errors.New("input too large")
				}
				return nil
			},
		}
		metadata, err := promptText.Run()
		if err != nil {
			return err
		}

		// Confirm action
		cont, err := promptContinue()
		if !cont || err != nil {
			return err
		}

		// Generate transaction
		submit, tx, _, err := cli.GenerateTransaction(ctx, nil, &actions.CreateAsset{
			Metadata: []byte(metadata),
		}, factory)
		if err != nil {
			return err
		}
		if err := submit(ctx); err != nil {
			return err
		}
		success, err := cli.WaitForTransaction(ctx, tx.ID())
		if err != nil {
			return err
		}
		printStatus(tx.ID(), success)
		return nil
	},
}

var mintAssetCmd = &cobra.Command{
	Use: "mint-asset",
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()
		_, priv, factory, cli, err := defaultActor()
		if err != nil {
			return err
		}

		// Select token to mint
		assetID, err := promptAsset("assetID", false)
		if err != nil {
			return err
		}
		exists, metadata, supply, owner, warp, err := cli.Asset(ctx, assetID)
		if err != nil {
			return err
		}
		if !exists {
			hutils.Outf("{{red}}%s does not exist{{/}}\n", assetID)
			hutils.Outf("{{red}}exiting...{{/}}\n")
			return nil
		}
		if warp {
			hutils.Outf("{{red}}cannot mint a warped asset{{/}}\n", assetID)
			hutils.Outf("{{red}}exiting...{{/}}\n")
			return nil
		}
		if owner != utils.Address(priv.PublicKey()) {
			hutils.Outf("{{red}}%s is the owner of %s, you are not{{/}}\n", owner, assetID)
			hutils.Outf("{{red}}exiting...{{/}}\n")
			return nil
		}
		hutils.Outf(
			"{{yellow}}metadata:{{/}} %s {{yellow}}supply:{{/}} %d\n",
			string(metadata),
			supply,
		)

		// Select recipient
		recipient, err := promptAddress("recipient")
		if err != nil {
			return err
		}

		// Select amount
		amount, err := promptAmount("amount", assetID, consts.MaxUint64-supply, nil)
		if err != nil {
			return err
		}

		// Confirm action
		cont, err := promptContinue()
		if !cont || err != nil {
			return err
		}

		// Generate transaction
		submit, tx, _, err := cli.GenerateTransaction(ctx, nil, &actions.MintAsset{
			Asset: assetID,
			To:    recipient,
			Value: amount,
		}, factory)
		if err != nil {
			return err
		}
		if err := submit(ctx); err != nil {
			return err
		}
		success, err := cli.WaitForTransaction(ctx, tx.ID())
		if err != nil {
			return err
		}
		printStatus(tx.ID(), success)
		return nil
	},
}

func submitDummy(
	ctx context.Context,
	cli *client.Client,
	dest crypto.PublicKey,
	factory chain.AuthFactory,
) error {
	var (
		logEmitted bool
		txsSent    uint64
	)
	for ctx.Err() == nil {
		_, h, t, err := cli.Accepted(ctx)
		if err != nil {
			return err
		}
		underHeight := h < dummyHeightThreshold
		if underHeight || time.Now().Unix()-t > dummyBlockAgeThreshold {
			if underHeight && !logEmitted {
				hutils.Outf(
					"{{yellow}}waiting for snowman++ activation (needed for AWM)...{{/}}\n",
				)
				logEmitted = true
			}
			submit, tx, _, err := cli.GenerateTransaction(ctx, nil, &actions.Transfer{
				To:    dest,
				Value: txsSent + 1, // prevent duplicate txs
			}, factory)
			if err != nil {
				return err
			}
			if err := submit(ctx); err != nil {
				return err
			}
			if _, err := cli.WaitForTransaction(ctx, tx.ID()); err != nil {
				return err
			}
			txsSent++
			time.Sleep(750 * time.Millisecond)
			continue
		}
		if logEmitted {
			hutils.Outf("{{yellow}}snowman++ activated{{/}}\n")
		}
		return nil
	}
	return ctx.Err()
}
