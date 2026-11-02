package cellnscoped

import (
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// admissionReadTTL bounds how long one admission's catalogue memo is kept. It
// covers the passes from preparation to the revalidation before start (well
// under the 60 s admission window); a memo that outlives it is dropped and the
// next resolution reads in full. Expiry only costs a full LIST: the memo is
// revalidated against live identities on every use (see
// cellnauthority.CatalogueReads), so its age never decides what is current.
const admissionReadTTL = 2 * time.Minute

// admissionReadLimit bounds the number of admissions memoised at once. Beyond
// it a new admission resolves without a memo, as before.
const admissionReadLimit = 1024

type admissionReadEntry struct {
	reads   *cellnauthority.CatalogueReads
	created time.Time
}

// admissionKey names one admission: the run, and for a continuation the turn.
func admissionKey(runUID, turnName string) string {
	return runUID + "/" + turnName
}

// admissionReads returns the catalogue memo for one admission, creating it if
// needed. It returns nil (no memo) when the run identity is unknown or the
// table is full.
func (d *Dispatcher) admissionReads(key string, now time.Time) *cellnauthority.CatalogueReads {
	if d == nil || key == "" || key[0] == '/' {
		return nil
	}
	d.readsMu.Lock()
	defer d.readsMu.Unlock()
	if d.reads == nil {
		d.reads = map[string]admissionReadEntry{}
	}
	if entry, ok := d.reads[key]; ok && now.Sub(entry.created) < admissionReadTTL {
		return entry.reads
	}
	if len(d.reads) >= admissionReadLimit {
		for k, entry := range d.reads {
			if now.Sub(entry.created) >= admissionReadTTL {
				delete(d.reads, k)
			}
		}
		if len(d.reads) >= admissionReadLimit {
			return nil
		}
	}
	entry := admissionReadEntry{reads: &cellnauthority.CatalogueReads{}, created: now}
	d.reads[key] = entry
	return entry.reads
}

// forgetAdmissionReads drops one admission's memo once its revalidation has run.
func (d *Dispatcher) forgetAdmissionReads(key string) {
	if d == nil {
		return
	}
	d.readsMu.Lock()
	defer d.readsMu.Unlock()
	delete(d.reads, key)
}
