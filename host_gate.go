package main

// hostGate bounds concurrent CGO host callbacks so a slow host cannot pile up
// blocked OS threads when many credential reads run in parallel.
import "errors"

const hostCallLimit = 16

var hostCallGate = make(chan struct{}, hostCallLimit)

var errHostGateClosed = errors.New("host call gate unavailable")

func hostGateAcquire() error {
	if hostCallGate == nil {
		return errHostGateClosed
	}
	hostCallGate <- struct{}{}
	return nil
}

func hostGateRelease() {
	if hostCallGate == nil {
		return
	}
	<-hostCallGate
}
