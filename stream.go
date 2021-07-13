package main

import (
	"errors"
)

type StreamManager struct {
	streams map[uint32]*ConnHandler
}

func NewStreamManager() StreamManager {
	return StreamManager{streams: make(map[uint32]*ConnHandler)}
}

func (mgr *StreamManager) AddStream(connHandler *ConnHandler) error {
	if _, exists := mgr.streams[connHandler.channelID]; exists {
		return errors.New("stream already exists in state")
	}
	mgr.streams[connHandler.channelID] = connHandler
	return nil
}

func (mgr *StreamManager) RemoveStream(id uint32) error {
	if _, exists := mgr.streams[id]; !exists {
		return errors.New("stream does not exist in state")
	}
	delete(mgr.streams, id)
	return nil
}
func (mgr *StreamManager) GetStream(id uint32) (*ConnHandler, error) {
	if _, exists := mgr.streams[id]; !exists {
		return &ConnHandler{}, errors.New("stream does not exist in state")
	}
	return mgr.streams[id], nil
}
