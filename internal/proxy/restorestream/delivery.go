package restorestream

// DeliveryCounts 按实际交付的逻辑位置计数，同一处的 done 和 completed 快照只计一次。
type DeliveryCounts struct {
	Text, Tool               int
	UnknownText, UnknownTool int
}

type deliveryRecorder interface {
	RecordDelivery(token string, tool bool, count int)
}

type countedValues struct {
	dict                  Dictionary
	recorder              deliveryRecorder
	pending               map[string]int
	count, unknown, limit int
}

func (d *countedValues) lookup(id string) (string, bool, error) {
	if d.dict == nil {
		return "", false, nil
	}
	v, ok := d.dict.Lookup(id)
	if state, checked := d.dict.(interface{ LookupError() error }); checked && state.LookupError() != nil {
		return "", false, ErrLocalCapacity
	}
	if ok {
		d.count++
		if d.recorder != nil {
			if d.pending == nil {
				d.pending = make(map[string]int)
			}
			d.pending[id]++
		}
	}
	return v, ok, nil
}

func (d *countedValues) confirm(tool bool) {
	for token, count := range d.pending {
		d.recorder.RecordDelivery(token, tool, count)
		delete(d.pending, token)
	}
}

type deliveryLedger struct {
	textValues, toolValues countedValues
	delivered              DeliveryCounts
}

func newLedger(d Dictionary, limit int) deliveryLedger {
	recorder, _ := d.(deliveryRecorder)
	counter := countedValues{dict: d, recorder: recorder, limit: limit}
	return deliveryLedger{textValues: counter, toolValues: counter}
}

func (l *deliveryLedger) values(role string) *countedValues {
	if role == "text" {
		return &l.textValues
	}
	return &l.toolValues
}

func (l *deliveryLedger) confirmDelivery() {
	l.textValues.confirm(false)
	l.toolValues.confirm(true)
	l.delivered = DeliveryCounts{
		Text: l.textValues.count, Tool: l.toolValues.count,
		UnknownText: l.textValues.unknown, UnknownTool: l.toolValues.unknown,
	}
}
