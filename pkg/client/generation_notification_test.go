package client

func (e *Engine) notifyGenerationState(g *connectionGeneration, state State) {
	if callback := e.OnStateChange; callback != nil {
		e.callbackMu.Lock()
		e.enqueueReliableGenerationCallbackLocked(g, func() { callback(state) })
		e.callbackMu.Unlock()
	}
}
