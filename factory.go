package vm

import (
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/vms"

	"github.com/rafael-abuawad/samplevm/controller"
)

var _ vms.Factory = &Factory{}

type Factory struct{}

func (*Factory) New(logging.Logger) (interface{}, error) {
	return controller.New(), nil
}
