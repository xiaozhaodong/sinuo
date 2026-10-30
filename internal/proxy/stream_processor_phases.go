package proxy

import "time"

func (sp *StreamProcessor) SetStreamPhaseObserver(observer func(*ParseResult, int, time.Time)) {
	sp.phaseObserver = observer
}

func (sp *StreamProcessor) observeStreamRead(data []byte) {
	if sp.phaseObserver != nil {
		sp.phaseObserver(nil, len(data), time.Now())
	}
}
