package capture

import (
	"encoding/json"
	"sort"
	"strings"
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
// name and metadata) is dropped. Live capture and pcap replay use it alike,
// so a replay stores what the live daemon would have stored. A Decoder is
// not safe for concurrent use.
type Decoder struct {
	protocols decoders.Protocols
	last      map[string]time.Time
	pruned    time.Time
}

// NewDecoder returns a decoder for the enabled protocols.
func NewDecoder(p decoders.Protocols) *Decoder {
	return &Decoder{protocols: p, last: map[string]time.Time{}}
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

// key identifies an observation's content apart from its time.
func key(o observation.Observation) string {
	var sb strings.Builder
	sb.WriteString(o.Interface)
	sb.WriteByte('|')
	sb.WriteString(string(o.Source))
	sb.WriteByte('|')
	sb.WriteString(o.MAC.String())
	sb.WriteByte('|')
	if o.IP.IsValid() {
		sb.WriteString(o.IP.String())
	}
	sb.WriteByte('|')
	sb.WriteString(string(o.NameType))
	sb.WriteByte(':')
	sb.WriteString(o.Hostname)
	keys := make([]string, 0, len(o.Meta))
	for k := range o.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteByte('|')
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(o.Meta[k])
	}
	// Name records and DHCP messages count whole: an mDNS goodbye differs
	// from the announcement only in its TTL, and a DHCP message of another
	// transaction, stage or retransmission (secs) is new evidence even for
	// the same client and address.
	if len(o.Records) > 0 || o.DHCP != nil {
		b, _ := json.Marshal(struct {
			R []observation.Record
			D *observation.DHCPMessage
		}{o.Records, o.DHCP})
		sb.WriteByte('|')
		sb.Write(b)
	}
	return sb.String()
}
