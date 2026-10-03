package pdcli

import "sync"

// limitedBuffer collects output up to max bytes. On overflow it keeps the
// first max bytes, records the overflow, and calls onOverflow once so the
// process can be stopped instead of filling memory.
type limitedBuffer struct {
	mu         sync.Mutex
	buf        []byte
	max        int
	overflowed bool
	onOverflow func()
	// onLine, when set, receives each complete line as it arrives (without "\n").
	onLine  func(string)
	pending []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n := len(p)
	room := b.max - len(b.buf)
	var fire func()
	switch {
	case room >= len(p):
		b.buf = append(b.buf, p...)
	default:
		if room > 0 {
			b.buf = append(b.buf, p[:room]...)
		}
		if !b.overflowed {
			b.overflowed = true
			fire = b.onOverflow
		}
	}
	var lines []string
	if b.onLine != nil && !b.overflowed {
		b.pending = append(b.pending, p...)
		for {
			i := indexByte(b.pending, '\n')
			if i < 0 {
				break
			}
			lines = append(lines, string(b.pending[:i]))
			b.pending = b.pending[i+1:]
		}
	}
	onLine := b.onLine
	b.mu.Unlock()

	for _, l := range lines {
		onLine(l)
	}
	if fire != nil {
		fire()
	}
	// Always report success: a short write would make os/exec abort the copy
	// and could block the child on a full pipe.
	return n, nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *limitedBuffer) Overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflowed
}

func indexByte(p []byte, c byte) int {
	for i, x := range p {
		if x == c {
			return i
		}
	}
	return -1
}
