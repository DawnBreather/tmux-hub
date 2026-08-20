//go:build !linux && !darwin

package hub

// peerOf answers nothing where the kernel offers no peer-credential option.
//
// Returning false rather than guessing is the whole point: `Dial` treats a zero peer as "not
// known", and every caller already handles that, so a new platform gets a working transport
// classification and loses only the corroboration.
func peerOf(_ uintptr) (PeerInfo, bool) { return PeerInfo{}, false }
