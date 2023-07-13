package controller

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/rafael-abuawad/samplevm/storage"
)

type StateManager struct{}

func (*StateManager) IncomingWarpKey(sourceChainID ids.ID, msgID ids.ID) []byte {
	return storage.IncomingWarpKeyPrefix(sourceChainID, msgID)
}

func (*StateManager) OutgoingWarpKey(txID ids.ID) []byte {
	return storage.OutgoingWarpKeyPrefix(txID)
}
