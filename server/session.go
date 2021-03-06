package server

/*
 * go-raknet
 *
 * Copyright (c) 2018 beito
 *
 * This software is released under the MIT License.
 * http://opensource.org/licenses/mit-license.php
 */

import (
	"errors"
	"net"
	"time"

	"github.com/beito123/go-raknet/binary"
	"github.com/beito123/go-raknet/protocol"
	"github.com/beito123/go-raknet/util"

	raknet "github.com/beito123/go-raknet"
)

//

var (
	errSessionClosed = errors.New("session closed")
)

type SessionState int

const (
	StateDisconected SessionState = iota
	StateHandshaking
	StateConnected
)

// Session
type Session struct {
	// Addr is the client's address to connect
	Addr *net.UDPAddr

	// Conn is a connection for the client
	Conn *net.UDPConn

	// Logger is a logger
	Logger raknet.Logger

	// Server is the server instance.
	Server *Server

	// GUID is session's GUID
	GUID int64

	// MTU is the max packet size to receive and send
	MTU int

	State SessionState

	// connectedTime is the time completed connection with client
	connectedTime time.Time

	// latencyEnabled enables measuring a latency time
	latencyEnabled bool

	// latencyTimestamps is timestamps of pong packet sent from client
	latencyTimestamps []int64

	// Latency is the average latency time data
	Latency *raknet.Latency

	// messageIndex is a message index of EncapsulatedPacket
	messageIndex binary.Triad

	// splitID is a handling split id
	splitID uint16

	// reliablePackets contains handled reliable packets
	reliablePackets map[binary.Triad]bool

	// splitQueue contains split packets with split id
	// It's used to handle split packets
	splitQueue map[uint16]*SplitPacket

	// sendQueue is a queue contained packets to send
	sendQueue *util.Queue // map[int]*protocol.EncapsulatedPacket

	// recoveryQueue is a recovery queue by NACK to send the client
	// When the session received NACK packet, add lost packets.
	recoveryQueue *util.OrderedMap // map[int][]*protocol.EncapsulatedPacket

	// ackReceiptPackets contains ack index of sent packets to the client
	ackReceiptPackets map[int]*protocol.EncapsulatedPacket

	// sendSequenceNumber is sent the newest sequence number to the client
	// It's used in CustomPacket
	sendSequenceNumber binary.Triad

	// receiveSequenceNumber is received the newest sequence number from the client
	// It's used in CustomPacket
	receiveSequenceNumber binary.Triad

	// orderSendIndex contains the newest sent order indexes to client with order channel.
	// It's used in EncapsulatedPacket
	orderSendIndex map[int]binary.Triad

	// orderReceiveIndex contains the newest received order indexes to client with order channel.
	// It's used in EncapsulatedPacket
	orderReceiveIndex map[int]binary.Triad

	// sequenceSendIndex contains the newest sent sequence indexes to client with order channel.
	// It's used in EncapsulatedPacket
	sequenceSendIndex map[int]binary.Triad

	// sequenceReceiveIndex contains the newest received sequence indexes to client with order channel.
	// It's used in EncapsulatedPacket
	sequenceReceiveIndex map[int]binary.Triad

	// handleQueue contains ordered packets with order channel
	// It's used to handle ordered packet in the order
	handleQueue map[byte]map[binary.Triad]*protocol.EncapsulatedPacket

	// PacketReceivedCount is sent a packet counter
	// It's used to check packets count on every second
	PacketSentCount int

	// PacketReceivedCount is received a packet counter
	// It's used to check packets count on every second
	PacketReceivedCount int

	// LastPacketSendTime is the last time sent a packet
	LastPacketSendTime time.Time

	// LastPacketReceiveTime is the last time received a packet
	LastPacketReceiveTime time.Time

	// LastRecoverySendTime is the last time sent a lost packet
	LastRecoverySendTime time.Time

	// LastKeepAliveSendTime is the last time sent DetectLostConnection packet
	LastKeepAliveSendTime time.Time

	// LastPacketCounterResetTime is the last time reset packet counters
	LastPacketCounterResetTime time.Time

	// LastPingSendTime is the last time sent a ping packet
	LastPingSendTime time.Time
}

func (session *Session) SystemAddress() *raknet.SystemAddress {
	return raknet.NewSystemAddressBytes([]byte(session.Addr.IP), uint16(session.Addr.Port))
}

func (session *Session) Init() {
	session.Latency = new(raknet.Latency)

	session.reliablePackets = make(map[binary.Triad]bool)
	session.splitQueue = make(map[uint16]*SplitPacket)

	session.sendQueue = util.NewQueue()
	session.recoveryQueue = util.NewOrderedMap()

	session.ackReceiptPackets = make(map[int]*protocol.EncapsulatedPacket)

	session.orderSendIndex = make(map[int]binary.Triad, raknet.MaxChannels)
	session.orderReceiveIndex = make(map[int]binary.Triad, raknet.MaxChannels)
	session.sequenceSendIndex = make(map[int]binary.Triad, raknet.MaxChannels)
	session.sequenceReceiveIndex = make(map[int]binary.Triad, raknet.MaxChannels)

	session.handleQueue = make(map[byte]map[binary.Triad]*protocol.EncapsulatedPacket)

	session.LastPacketSendTime = time.Now()
	session.LastPacketReceiveTime = time.Now()
	session.LastRecoverySendTime = time.Now()
	session.LastKeepAliveSendTime = time.Now()
	session.LastPacketCounterResetTime = time.Now()
}

// Timestamp returns a time from a time connected
// used for Ping and Pong packets
func (session *Session) Timestamp() int64 {
	return int64(time.Now().Sub(session.connectedTime))
}

func (session *Session) handlePacket(pk raknet.Packet, channel int) {
	if session.State == StateDisconected {
		return
	}

	switch npk := pk.(type) {
	case *protocol.ConnectedPing:
		err := npk.Decode()
		if err != nil {
			session.Logger.Warn(err)
			return
		}

		pong := &protocol.ConnectedPong{
			Timestamp:     npk.Timestamp,
			TimestampPong: session.Timestamp(),
		}

		err = pong.Encode()
		if err != nil {
			session.Logger.Warn(err)
			return
		}

		_, err = session.SendPacket(pong, raknet.Unreliable, channel)
		if err != nil {
			session.Logger.Warn(err)
		}
	case *protocol.ConnectedPong:
		err := npk.Decode()
		if err != nil {
			session.Logger.Warn(err)
			return
		}

		if session.latencyEnabled {
			unset := func(v []int64, i int) []int64 {
				return append(v[:i], v[i+1:]...)
			}

			now := session.Timestamp()
			for i, ts := range session.latencyTimestamps {
				if ts == npk.Timestamp {
					unset(session.latencyTimestamps, i)

					session.Latency.AddRaw(session.LastPacketReceiveTime.Sub(session.LastPingSendTime))
				} else {
					if time.Duration(now-ts) >= raknet.SessionTimeout || len(session.latencyTimestamps) > 10 {
						unset(session.latencyTimestamps, i)
					}
				}
			}
		}
	case *protocol.ConnectionRequestAccepted:
		if session.State != StateHandshaking {
			return
		}

		err := npk.Decode()
		if err != nil {
			session.Logger.Warn(err) // remove

			session.Server.CloseSession(session.Addr, "Failed to login")
			return
		}

		hpk := &protocol.NewIncomingConnection{
			ServerAddress:   session.SystemAddress(),
			ClientTimestamp: npk.ServerTimestamp,
			ServerTimestamp: npk.ClientTimestamp,
		}

		err = hpk.Encode()
		if err != nil {
			session.Server.CloseSession(session.Addr, "Failed to login")
			return
		}

		_, err = session.SendPacket(hpk, raknet.ReliableOrderedWithACKReceipt, channel)
		if err != nil {
			session.Logger.Warn(err)
		}

		session.State = StateConnected
		session.connectedTime = time.Now()

		for _, handler := range session.Server.Handlers {
			handler.OpenedConn(session.GUID, session.Addr)
		}
	case *protocol.DisconnectionNotification:
		err := npk.Decode()
		if err != nil {
			session.Logger.Warn(err)
			return
		}

		session.Server.CloseSession(session.Addr, "Server disconnected")
	default:
		if npk.ID() >= protocol.IDUserPacketEnum { // user packet
			for _, hand := range session.Server.Handlers {
				hand.HandlePacket(session.GUID, npk)
			}
		} else { // unknown packet
			for _, hand := range session.Server.Handlers {
				hand.HandleUnknownPacket(session.GUID, npk)
			}
		}
	}
}

func (session *Session) handleCustomPacket(cpk *protocol.CustomPacket) {
	if session.State == StateDisconected {
		return
	}

	for _, handler := range session.Server.Handlers { // Debug: I'll remove
		handler.HandlePacket(session.GUID, cpk)
	}

	session.PacketReceivedCount++

	// Generate NACK if needed
	diff := cpk.Index - session.receiveSequenceNumber
	if diff > 1 { // it need a serial number
		if diff > 2 {
			session.sendACK(protocol.TypeNACK, &raknet.Record{
				Index:    int(session.receiveSequenceNumber.Add(1)),
				EndIndex: int(cpk.Index.Sub(1)),
			})
		} else {
			session.sendACK(protocol.TypeNACK, &raknet.Record{
				Index: int(cpk.Index.Sub(1)),
			})
		}
	}

	// Handle epks if it's a newer packet
	if cpk.Index >= session.receiveSequenceNumber {
		session.receiveSequenceNumber = cpk.Index
		for _, epk := range cpk.Messages {
			session.handleEncapsulated(epk)
		}

		session.LastPacketReceiveTime = time.Now()
	}

	// Send ACK
	session.sendACK(protocol.TypeACK, &raknet.Record{
		Index: int(cpk.Index),
	})
}

func (session *Session) handleACKPacket(pk *protocol.Acknowledge) {
	if session.State == StateDisconected {
		return
	}

	switch pk.Type {
	case protocol.TypeACK:
		for _, record := range pk.Records {
			index := record.Index

			_, ok := session.ackReceiptPackets[index]
			if ok {
				delete(session.ackReceiptPackets, index)
			}

			session.recoveryQueue.Remove(index)
		}
	case protocol.TypeNACK:
		for i := 0; i < len(pk.Records); i++ {
			record := pk.Records[i]
			index := record.Index

			// If the packet is unreliable, send lost packets
			// but don't send after that
			p, ok := session.ackReceiptPackets[index]
			if ok && !p.Reliability.IsReliable() {
				delete(session.ackReceiptPackets, index)
			}

			if session.existRecoveryQueue(index) {
				epks, ok := session.getRecoveryQueue(index)
				if ok {
					break
				}

				nindex, err := session.SendCustomPacket(epks, false)
				if err != nil {
					session.Logger.Warn(err)
					break
				}

				session.renameRecoveryQueue(index, nindex)
			}
		}
	}

	session.LastPacketReceiveTime = time.Now()
}

func (session *Session) handleEncapsulated(epk *protocol.EncapsulatedPacket) {
	reliability := epk.Reliability

	if epk.Split {
		spk, ok := session.splitQueue[epk.SplitID]
		if !ok {
			// If the queue is full, removes unreliable packet to make space
			if len(session.splitQueue)+1 > raknet.MaxSplitsPerQueue {
				// Remove unreliable packets from split queue
				for key, pk := range session.splitQueue {
					if !pk.Reliability.IsReliable() {
						delete(session.splitQueue, key)
					}
				}

				if len(session.splitQueue)+1 > raknet.MaxSplitsPerQueue {
					session.Logger.Warn("Failed to make space of split queue")
					return
				}
			}

			session.splitQueue[epk.SplitID] = &SplitPacket{
				SplitID:     int(epk.SplitID),
				SplitCount:  int(epk.SplitCount),
				Reliability: epk.Reliability,
			}

			spk = session.splitQueue[epk.SplitID]
		}

		// Add split packet and get complete payload if it's completed
		payload := spk.Update(epk)
		if payload == nil {
			return
		}

		epk.Payload = payload
		delete(session.splitQueue, epk.SplitID)
	}

	// Make sure we are not handling a duplicate
	if reliability.IsReliable() {
		_, ok := session.reliablePackets[epk.MessageIndex]
		if ok {
			return
		}

		session.reliablePackets[epk.MessageIndex] = true
	}

	if epk.OrderChannel >= raknet.MaxChannels {
		session.Logger.Warn("Invalid channel")
		return
	}

	if reliability.IsOrdered() {
		queue := session.handleQueue[epk.OrderChannel]

		queue[epk.OrderIndex] = epk

		index := session.orderReceiveIndex[int(epk.OrderChannel)]
		for {
			p, ok := queue[index]
			if !ok {
				break
			}

			delete(session.handleQueue[epk.OrderChannel], index)

			index++

			session.handlePacket(protocol.NewRawPacket(p.Payload), int(epk.OrderChannel))
		}
	} else if reliability.IsSequenced() {
		if epk.OrderIndex > session.sequenceReceiveIndex[int(epk.OrderChannel)] {
			session.sequenceReceiveIndex[int(epk.OrderChannel)] = epk.OrderIndex
			session.handlePacket(protocol.NewRawPacket(epk.Payload), int(epk.OrderChannel))
		}
	} else {
		session.handlePacket(protocol.NewRawPacket(epk.Payload), int(epk.OrderChannel))
	}
}

func (session *Session) addSendQueue(epk *protocol.EncapsulatedPacket) {
	session.sendQueue.Add(epk)
}

func (session *Session) bumpOrderSendIndex(channel int) (index binary.Triad) {
	index = session.orderSendIndex[channel]
	return BumpTriad(&index)
}

func (session *Session) bumpSequenceSendIndex(channel int) (index binary.Triad) {
	index = session.sequenceSendIndex[channel]
	return BumpTriad(&index)
}

func (session *Session) getRecoveryQueue(index int) ([]*protocol.EncapsulatedPacket, bool) {
	v, ok := session.recoveryQueue.Get(index)
	if !ok {
		return nil, false
	}

	epks, ok := v.([]*protocol.EncapsulatedPacket)
	if !ok {
		panic("Invalid value, wants []*protocol.EncapsulatedPacket")
	}

	return epks, true
}

func (session *Session) setRecoveryQueue(index int, epks []*protocol.EncapsulatedPacket) {
	session.recoveryQueue.Set(index, epks)
}

func (session *Session) removeRecoveryQueue(index int) {
	session.recoveryQueue.Remove(index)
}

func (session *Session) existRecoveryQueue(index int) bool {
	return session.recoveryQueue.Exist(index)
}

func (session *Session) renameRecoveryQueue(from int, to int) {
	epks, ok := session.getRecoveryQueue(from)
	if !ok {
		return
	}

	session.setRecoveryQueue(to, epks)
	session.removeRecoveryQueue(from)
}

func (session *Session) pollRecoveryQueue() (epks []*protocol.EncapsulatedPacket, ok bool) {
	value, ok := session.recoveryQueue.Pop()
	if !ok {
		return nil, false
	}

	epks, ok = value.([]*protocol.EncapsulatedPacket)
	if !ok {
		return nil, false
	}

	return epks, true
}

func (session *Session) SendPacket(pk raknet.Packet, reliability raknet.Reliability, channel int) (protocol.EncapsulatedPacket, error) {
	return session.SendPacketBytes(pk.Bytes(), reliability, channel)
}

func (session *Session) SendPacketBytes(b []byte, reliability raknet.Reliability, channel int) (protocol.EncapsulatedPacket, error) {
	if channel >= raknet.MaxChannels {
		return protocol.EncapsulatedPacket{}, errors.New("invalid channel")
	}

	epk := &protocol.EncapsulatedPacket{
		Reliability:  reliability,
		OrderChannel: byte(channel),
		Payload:      b,
	}

	if reliability.IsReliable() {
		epk.MessageIndex = BumpTriad(&session.messageIndex)
	}

	if reliability.IsOrdered() || reliability.IsSequenced() {
		if reliability.IsOrdered() {
			epk.OrderIndex = session.bumpOrderSendIndex(channel)
		} else {
			epk.OrderIndex = session.bumpSequenceSendIndex(channel)
		}

		//session.Logger.Debug("Bumped" + )
	}

	if needSplit(epk.Reliability, b, session.MTU) {
		epk.SplitID = BumpUInt16(&session.splitID)

		for _, spk := range session.splitPacket(epk) {
			session.addSendQueue(spk)
		}
	} else {
		session.addSendQueue(epk)
	}

	return *epk, nil // returns clone of epk
}

func (session *Session) SendCustomPacket(epks []*protocol.EncapsulatedPacket, updateRecoveryQueue bool) (int, error) {
	cpk := protocol.NewCustomPacket(protocol.IDCustom4)
	cpk.Index = BumpTriad(&session.sendSequenceNumber)
	cpk.Messages = epks

	err := cpk.Encode()
	if err != nil {
		return 0, err
	}

	for _, epk := range cpk.Messages {
		session.ackReceiptPackets[epk.Record.Index] = epk
	}

	session.SendRawPacket(cpk)

	if updateRecoveryQueue {
		cpk.RemoveUnreliables()
		if len(cpk.Messages) > 0 {
			session.setRecoveryQueue(int(cpk.Index), cpk.Messages)
		}
	}

	session.PacketSentCount++
	session.LastPacketSendTime = time.Now()

	return int(cpk.Index), nil
}

func (session *Session) splitPacket(epk *protocol.EncapsulatedPacket) []*protocol.EncapsulatedPacket {
	exp := util.SplitBytesSlice(epk.Payload,
		session.MTU-(protocol.CalcCPacketBaseSize()+protocol.CalcEPacketSize(epk.Reliability, true, []byte{})))

	spk := make([]*protocol.EncapsulatedPacket, len(exp))

	for i := 0; i < len(exp); i++ {
		npk := &protocol.EncapsulatedPacket{
			Reliability: epk.Reliability,
			Payload:     exp[i],
		}

		if epk.Reliability.IsReliable() {
			npk.MessageIndex = BumpTriad(&session.messageIndex)
		} else {
			npk.MessageIndex = epk.MessageIndex
		}

		if epk.Reliability.IsOrdered() || epk.Reliability.IsSequenced() {
			npk.OrderChannel = epk.OrderChannel
			npk.OrderIndex = epk.OrderIndex
		}

		npk.Split = true
		npk.SplitCount = int32(len(exp))
		npk.SplitID = epk.SplitID
		npk.SplitIndex = int32(i)

		spk[i] = npk
	}

	return spk
}

func (session *Session) sendACK(typ protocol.ACKType, records ...*raknet.Record) {
	ack := &protocol.Acknowledge{
		Type:    typ,
		Records: records,
	}

	err := ack.Encode()
	if err != nil {
		session.Logger.Warn(err)

		return
	}

	session.SendRawPacket(ack)

	session.LastPacketSendTime = time.Now()
}

func (session *Session) SendRawPacket(pk raknet.Packet) {
	session.Server.SendRawPacket(session.Addr, pk.Bytes())
}

func (session *Session) update() bool {
	if session.State == StateDisconected {
		return false
	}

	current := time.Now()

	// send packets in the send queue
	if !session.sendQueue.IsEmpty() && session.PacketSentCount < raknet.MaxPacketsPerSecond {
		var send []*protocol.EncapsulatedPacket
		sendLen := protocol.CalcCPacketBaseSize()

		session.sendQueue.Range(func(value interface{}) bool {
			epk, ok := value.(*protocol.EncapsulatedPacket)
			if !ok {
				return true
			}

			sendLen += epk.CalcSize()
			if sendLen > session.MTU {
				return false
			}

			send = append(send, epk)
			session.sendQueue.Remove()

			return true
		})

		if len(send) > 0 {
			session.SendCustomPacket(send, true)
		}
	}

	// resend lost packets
	if current.Sub(session.LastRecoverySendTime) >= raknet.RecoverySendInterval {
		pks, ok := session.pollRecoveryQueue()
		if ok {
			session.SendCustomPacket(pks, false)
			session.LastRecoverySendTime = time.Now()
		}
	}

	// Send ping to detect latency if it is enabled
	if session.latencyEnabled && current.Sub(session.LastPingSendTime) >= raknet.PingSendInterval &&
		session.State == StateConnected {
		ping := &protocol.ConnectedPing{
			Timestamp: session.Timestamp(),
		}

		err := ping.Encode()
		if err != nil {
			session.Logger.Warn(err)
		} else {
			session.SendPacket(ping, raknet.Unreliable, raknet.DefaultChannel)
			session.LastPingSendTime = current
			session.latencyTimestamps = append(session.latencyTimestamps, ping.Timestamp)
		}

	}

	if current.Sub(session.LastPacketReceiveTime) >= raknet.DetectionSendInterval &&
		current.Sub(session.LastKeepAliveSendTime) >= raknet.DetectionSendInterval &&
		session.State == StateConnected {

		session.SendPacket(&protocol.DetectLostConnections{}, raknet.Unreliable, raknet.DefaultChannel)
		session.LastKeepAliveSendTime = time.Now()

		session.Logger.Debug("Sent DetectLostConnections packet to the client")
	}

	// Time out
	if current.Sub(session.LastPacketReceiveTime) >= raknet.SessionTimeout {
		for _, handler := range session.Server.Handlers {
			handler.Timedout(session.GUID)
		}

		return false
	}

	// Reset counters
	if current.Sub(session.LastPacketCounterResetTime) >= 1000 {
		session.PacketSentCount = 0
		session.PacketReceivedCount = 0
		session.LastPacketCounterResetTime = current
	}

	return true
}

// Close closes the session
func (session *Session) Close() error {
	if session.State == StateDisconected {
		return errSessionClosed
	}

	//session.Server.CloseSession(session.UUID, "Disconnected from server")

	session.sendQueue.Clear()

	// send a disconnection notification packet
	session.SendPacket(&protocol.DisconnectionNotification{}, raknet.Unreliable, 0)

	session.update()

	session.State = StateDisconected

	return nil
}
