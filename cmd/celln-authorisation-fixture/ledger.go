package main

type modelLedger struct {
	run          Cap
	turn         Cap
	usedRun      Cap
	usedTurns    map[string]Cap
	reservations map[string]string
}

func newLedger(run, turn Cap) *modelLedger {
	return &modelLedger{run: run, turn: turn, usedTurns: map[string]Cap{}, reservations: map[string]string{}}
}
func (l *modelLedger) reserve(turnID, id, digest string, out int64) string {
	key := turnID + "/" + id
	if old, ok := l.reservations[key]; ok {
		if old == digest {
			return "recover"
		}
		return ReasonRequestConflict
	}
	u := l.usedTurns[turnID]
	if l.usedRun.Requests+1 > l.run.Requests || u.Requests+1 > l.turn.Requests || l.usedRun.OutputTokens+out > l.run.OutputTokens || u.OutputTokens+out > l.turn.OutputTokens {
		return ReasonBudgetExhausted
	}
	l.reservations[key] = digest
	l.usedRun.Requests++
	l.usedRun.OutputTokens += out
	u.Requests++
	u.OutputTokens += out
	l.usedTurns[turnID] = u
	return "accept"
}
