package srt

func (ln *listener) Accept2() (ConnRequest, error) {
	if ln.isShutdown() {
		return nil, ErrListenerClosed
	}

	for {
		select {
		case <-ln.doneChan:
			return nil, ln.error()

		case p := <-ln.backlog:
			req := newConnRequest(ln, p)
			if req == nil {
				break
			}

			return req, nil
		}
	}
}

func (ln *listener) Accept(acceptFn AcceptFunc) (Conn, ConnType, error) {
	for {
		req, err := ln.Accept2()
		if err != nil {
			return nil, REJECT, err
		}

		if acceptFn == nil {
			req.Reject(REJ_PEER)
			continue
		}

		mode := acceptFn(req)
		if mode != PUBLISH && mode != SUBSCRIBE {
			// Figure out the reason
			reason := REJ_PEER
			if req.(*connRequest).rejectionReason > 0 {
				reason = req.(*connRequest).rejectionReason
			}
			req.Reject(reason)
			continue
		}

		conn, err := req.Accept()
		if err != nil {
			continue
		}

		return conn, mode, nil
	}
}

