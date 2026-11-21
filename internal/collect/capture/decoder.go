package capture

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/otfabric/lan-tracker/internal/collect/capture/decoders"
	"github.com/otfabric/lan-tracker/internal/observation"
)

// RefreshInterval is how often an unchanged observation is emitted again.
// Chatty hosts repeat the same evidence many times a second; one
// observation per interval keeps presence fresh (ACTIVE is 5 min by
// default) while bounding the observation volume.
const RefreshInterval = 2 * time.Minute

// maxTracked bounds the refresh table; when it is full after pruning, the
// table is cleared (which only lets a few repeats through).
const maxTracked = 16384

// Decoder decodes frames and suppresses repeats: an observation identical
// to one emitted less than RefreshInterval earlier (same source, MAC, IP,
// name, metadata, records and DHCP message) is dropped. The table keys
// are fixed-size digests of that content. Live capture and pcap replay use it alike,
// so a replay stores what the live daemon would have stored. A Decoder is
// not safe for concurrent use.
type Decoder struct {
	protocols decoders.Protocols
	last      map[digest]time.Time
	pruned    time.Time
}

// NewDecoder returns a decoder for the enabled protocols.
func NewDecoder(p decoders.Protocols) *Decoder {
	return &Decoder{protocols: p, last: map[digest]time.Time{}}
}

// Decode returns the new or refreshed observations in one frame.
func (d *Decoder) Decode(t time.Time, iface string, frame []byte) []observation.Observation {
	obs := decoders.Decode(d.protocols, t, iface, frame)
	if len(obs) == 0 {
		return nil
	}
	d.prune(t)
	out := obs[:0]
	for _, o := range obs {
		k := key(o)
		if prev, ok := d.last[k]; ok && !t.Before(prev) && t.Sub(prev) < RefreshInterval {
			continue
		}
		d.last[k] = t
		out = append(out, o)
	}
	return out
}

// Forget drops o from the refresh table, so the next identical frame
// produces it again (used when o could not be delivered).
func (d *Decoder) Forget(o observation.Observation) { delete(d.last, key(o)) }

func (d *Decoder) prune(t time.Time) {
	if t.Sub(d.pruned) < RefreshInterval && len(d.last) < maxTracked {
		return
	}
	d.pruned = t
	for k, prev := range d.last {
		if t.Sub(prev) >= RefreshInterval || t.Before(prev) {
			delete(d.last, k)
		}
	}
	if len(d.last) >= maxTracked {
		clear(d.last)
	}
}

// digest is a refresh-table key: a SHA-256 of the observation's content,
// so every entry has the same small size however many records the
// observation carries.
type digest [sha256.Size]byte

// key identifies an observation's content apart from its time. Each field
// is written with its length, so two observations share a key only when
// their content is identical.
func key(o observation.Observation) digest {
	h := sha256.New()
	field := func(s string) {
		_, _ = io.WriteString(h, strconv.Itoa(len(s)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, s)
	}
	field(o.Interface)
	field(string(o.Source))
	field(o.MAC.String())
	ip := ""
	if o.IP.IsValid() {
		ip = o.IP.String()
	}
	field(ip)
	field(string(o.NameType))
	field(o.Hostname)
	keys := make([]string, 0, len(o.Meta))
	for k := range o.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	field(strconv.Itoa(len(keys)))
	for _, k := range keys {
		field(k)
		field(o.Meta[k])
	}
	// Name records and DHCP messages count whole: an mDNS goodbye differs
	// from the announcement only in its TTL, and a DHCP message of another
	// transaction, stage or retransmission (secs) is new evidence even for
	// the same client and address. They are streamed into the hash, not
	// kept.
	if len(o.Records) > 0 || o.DHCP != nil {
		_ = json.NewEncoder(h).Encode(struct {
			R []observation.Record
			D *observation.DHCPMessage
		}{o.Records, o.DHCP})
	}
	var d digest
	h.Sum(d[:0])
	return d
}
