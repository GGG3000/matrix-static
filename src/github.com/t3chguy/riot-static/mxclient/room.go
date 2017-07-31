// Copyright 2017 Michael Telatynski <7t3chguy@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mxclient

import (
	"errors"
	"github.com/matrix-org/gomatrix"
	"github.com/t3chguy/riot-static/utils"
)

type RoomInfo struct {
	RoomID          string
	Name            string
	Topic           string
	AvatarURL       MXCURL
	NumMemberEvents int
	NumMembers      int
	NumServers      int
}

type Room struct {
	// each room has a client that is responsible for its state being up to date
	client *Client

	ID string

	backPaginationToken    string
	forwardPaginationToken string

	eventList []gomatrix.Event
	//eventMap        map[string]*gomatrix.Event
	latestRoomState RoomState

	hasInitialSynced bool
}

func (r *Room) ForwardPaginateRoom() {
	r.client.forwardpaginateRoom(r, 0)
}

func (r *Room) concatBackpagination(oldEvents []gomatrix.Event, newToken string) {
	for _, event := range oldEvents {
		if event.Type == "m.room.redaction" {
			// The server has already handled these for us
			// so just consume them to prevent them blanking on timeline
			continue
		}

		r.eventList = append(r.eventList, event)
	}
	r.backPaginationToken = newToken
}

func (r *Room) concatForwardPagination(newEvents []gomatrix.Event, newToken string) {
	for _, event := range newEvents {
		if event.Type == "m.room.redaction" {
			// TODO Handle redaction and skip adding to TL
			// Might want an Event Map->*Event so we can skip an O(n) task
			continue
		}

		r.latestRoomState.UpdateOnEvent(&event, false)
		r.eventList = append([]gomatrix.Event{event}, r.eventList...)
	}
	r.forwardPaginationToken = newToken
}

func (r *Room) GetTokens() (string, string) {
	return r.backPaginationToken, r.forwardPaginationToken
}

func (r *Room) findEventIndex(anchor string, backpaginate bool) (int, bool) {
	for index, event := range r.eventList {
		if event.ID == anchor {
			return index, true
		}
	}

	if backpaginate {
		if numNew, _ := r.client.backpaginateRoom(r, 100); numNew > 0 {
			return r.findEventIndex(anchor, false)
		}
	}
	return 0, false
}

// overcompenesatePaginationBy, number to try and keep as a buffer at the end of our in-memory timeline so we don't
// backpaginate on every single call.
const overcompensateBackpaginationBy = 32

func (r *Room) getBackwardEventRange(anchorIndex, offset, number int) []gomatrix.Event {
	length := len(r.eventList)

	// delta is the number of events we should have, to comfortably handle this request, if we do not have this many
	// then ask the mxclient to backpaginate this room by at least delta-length events.
	// TODO if numNew = 0, we are at end of TL as we know it, mark this room as such.
	if delta := anchorIndex + offset + number + overcompensateBackpaginationBy; delta >= length {
		// if no error encountered then we have new events, update our previously calculated length by the len of these.
		if numNew, err := r.client.backpaginateRoom(r, delta-length); err == nil {
			length += numNew
		}
	}

	startIndex := utils.Min(anchorIndex+offset, length)
	return r.eventList[startIndex:utils.Min(startIndex+number, length)]
}

func (r *Room) getForwardEventRange(index, offset, number int) []gomatrix.Event {
	length := len(r.eventList)
	topIndex := utils.Bound(0, index+number-offset, length)

	return r.eventList[utils.Max(topIndex-number, 0):topIndex]
}

func (r *Room) GetState() RoomState {
	return r.latestRoomState
}

func (r *Room) GetEventPage(anchor string, offset int, pageSize int) (events []gomatrix.Event, err error) {
	var anchorIndex int
	if anchor != "" {
		if index, found := r.findEventIndex(anchor, false); found {
			anchorIndex = index
		} else {
			return []gomatrix.Event{}, errors.New("Could not find event")
		}
	}

	if offset >= 0 {
		return r.getBackwardEventRange(anchorIndex, offset, pageSize), nil
	} else {
		return r.getForwardEventRange(anchorIndex, -offset, pageSize), nil
	}

	return
}

const RoomInitialSyncLimit = 256

func (m *Client) NewRoom(roomID string) (*Room, error) {
	resp, err := m.RoomInitialSync(roomID, RoomInitialSyncLimit)

	if err != nil {
		return nil, err
	}

	// filter out m.room.redactions and reverse ordering at once.
	var filteredEventList []gomatrix.Event
	for _, event := range resp.Messages.Chunk {
		if event.Type != "m.room.redaction" {
			continue
		}

		filteredEventList = append([]gomatrix.Event{event}, filteredEventList...)
	}

	newRoom := &Room{
		client: m,
		ID:     roomID,
		forwardPaginationToken: resp.Messages.End,
		backPaginationToken:    resp.Messages.Start,
		eventList:              filteredEventList,
		latestRoomState:        *NewRoomState(m),
	}

	for _, event := range resp.State {
		newRoom.latestRoomState.UpdateOnEvent(&event, true)
	}

	return newRoom, nil
}

func (r *Room) RoomInfo() RoomInfo {
	return RoomInfo{
		r.ID,
		r.latestRoomState.CalculateName(),
		r.latestRoomState.Topic,
		r.latestRoomState.AvatarURL,
		r.latestRoomState.GetNumMemberEvents(),
		r.latestRoomState.NumMembers(),
		len(r.latestRoomState.Servers()),
	}
}
