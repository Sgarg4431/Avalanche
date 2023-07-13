package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rafael-abuawad/samplevm/consts"
	"github.com/rafael-abuawad/samplevm/version"
)

func init() {
	cobra.EnablePrefixMatching = true
}

// NewCommand implements "tokenvm version" command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Prints out the verson",
		RunE:  versionFunc,
	}
	return cmd
}

func versionFunc(*cobra.Command, []string) error {
	fmt.Printf("%s@%s (%s)\n", consts.Name, version.Version, consts.ID)
	return nil
}
