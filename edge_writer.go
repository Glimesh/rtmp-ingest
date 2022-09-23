package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

const bufSize = 1000

type edgeWriter struct {
	buffers map[string]chan []byte

	mutex sync.RWMutex

	quit bool
}

func NewEdgeWriter() *edgeWriter {
	return &edgeWriter{
		buffers: make(map[string]chan []byte),
		mutex:   sync.RWMutex{},
		quit:    false,
	}
}

func (edge *edgeWriter) new(ctx context.Context, host string, conn net.Conn) {
	edge.buffers[host] = make(chan []byte, bufSize)

	_, cancel := context.WithCancel(ctx)

	go func() {
		for {
			if _, ok := edge.buffers[host]; !ok {
				return
			}

			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(<-edge.buffers[host]); err != nil {
				fmt.Println(err)
				cancel()
			}
		}
	}()
}

func (edge *edgeWriter) write(buf []byte) {
	for host := range edge.buffers {
		// if edge.quit {
		// 	return
		// }

		// Bug is the writes continue even after the edge is closed, likely filling up the buffer

		// fmt.Println("Writing to buffer for ", host)
		if len(edge.buffers[host]) >= bufSize {
			fmt.Printf("BUFFER FULL FOR %s, PURGING...\n", host)
			// If the queue is full, read the first message to start clearing it
			<-edge.buffers[host]
		}

		edge.buffers[host] <- buf
	}
}

func (edge *edgeWriter) remove(host string) {
	delete(edge.buffers, host)
}
