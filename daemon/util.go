package daemon

import "encoding/binary"

// the parseDims method and Winsize struct are taken from https://gist.github.com/jpillora/b480fde82bff51a06238.
func parseDims(b []byte) (w uint32, h uint32) {
	w := binary.BigEndian.Uint32(b)
	h := binary.BigEndian.Uint32(b[4:])
	return w, h
}
