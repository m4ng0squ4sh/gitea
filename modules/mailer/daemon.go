// Copyright 2017 The Gitea Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package mailer

import (
	"fmt"
	"sync"
	"time"

	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/setting"

	"github.com/desertbit/timer"
)

const (
	keepaliveTimeout = 30 * time.Second
)

// Daemon implements an asynchronous mail service daemon.
type Daemon struct {
	mailQueue chan *Message

	closeMutex sync.Mutex
	closeChan  chan struct{}
}

// NewDaemon create a new mail daemon.
func NewDaemon() (*Daemon, error) {
	queueLen := setting.MailService.QueueLength
	workers := setting.MailService.Workers

	// Validate input.
	if queueLen < 0 {
		return nil, fmt.Errorf("mail daemon: invalid queue length: %v", queueLen)
	} else if workers < 1 {
		return nil, fmt.Errorf("mail daemon: invalid workers routines: %v", workers)
	}

	d := &Daemon{
		mailQueue: make(chan *Message, queueLen),
		closeChan: make(chan struct{}),
	}

	// Create a sender for each mail worker routine.
	for i := 0; i < workers; i++ {
		s, err := createSender()
		if err != nil {
			return nil, err
		}

		go d.processMailQueue(s)
	}

	return d, nil
}

// IsClosed returns a boolean indicating if the daemon is closed.
// This method is thread-safe.
func (d *Daemon) IsClosed() bool {
	select {
	case <-d.closeChan:
		return true
	default:
		return false
	}
}

// Close the daemon and top all routines.
// This method is thread-safe.
func (d *Daemon) Close() {
	d.closeMutex.Lock()
	defer d.closeMutex.Unlock()

	// Check if already closed.
	if d.IsClosed() {
		return
	}

	// Release routines.
	close(d.closeChan)
}

// SendAsync send mail asynchronous.
func (d *Daemon) SendAsync(msg *Message) {
	// TODO: think about removing the extra goroutine an
	//       drop mails if the channel is full/flooded.
	go func() {
		// Don't block if closed.
		select {
		case <-d.closeChan:
		case d.mailQueue <- msg:
		}
	}()
}

func (d *Daemon) processMailQueue(s Sender) {
	var err error

	// Our close connection timer.
	t := timer.NewStoppedTimer()
	defer t.Stop()

	for {
		select {
		case <-d.closeChan:
			if err = s.Close(); err != nil {
				log.Error(3, "Failed to close mail sender connection: %v", err)
			}
			return

		case msg := <-d.mailQueue:
			log.Trace("New e-mails sending request %s: %s", msg.GetHeader("To"), msg.Info)
			if err = s.Send(msg); err != nil {
				log.Error(3, "Failed to send emails %s: %s - %v", msg.GetHeader("To"), msg.Info, err)
			} else {
				log.Trace("E-mails sent %s: %s", msg.GetHeader("To"), msg.Info)
			}

			// Reset the keepalive timeout timer.
			t.Reset(keepaliveTimeout)

		// Close the mail server connection if no email was sent within the timeout.
		case <-t.C:
			if err = s.Close(); err != nil {
				log.Error(3, "Failed to close mail sender connection: %v", err)
			}
		}
	}
}
