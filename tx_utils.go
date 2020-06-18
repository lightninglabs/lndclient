package lndclient

import (
	"bytes"

	"github.com/btcsuite/btcd/wire"
)

// encodeTx encodes a tx to raw bytes.
func encodeTx(tx *wire.MsgTx) ([]byte, error) {
	var buffer bytes.Buffer
	err := tx.BtcEncode(&buffer, 0, wire.WitnessEncoding)
	if err != nil {
		return nil, err
	}
	rawTx := buffer.Bytes()

	return rawTx, nil
}

// decodeTx decodes raw tx bytes.
func decodeTx(rawTx []byte) (*wire.MsgTx, error) {
	tx := wire.MsgTx{}
	r := bytes.NewReader(rawTx)
	err := tx.BtcDecode(r, 0, wire.WitnessEncoding)
	if err != nil {
		return nil, err
	}

	return &tx, nil
}
