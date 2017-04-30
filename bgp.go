package main

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/k-sone/critbitgo"
)

const (
	// BGP Version (RFC4271)
	bgpVersion byte = 4

	// BGP Message Types
	bgpTypeOPEN         byte = 1
	bgpTypeUPDATE       byte = 2
	bgpTypeNOTIFICATION byte = 3
	bgpTypeKEEPALIVE    byte = 4
	bgpTypeROUTEREFRESH byte = 5

	// BGP States
	bgpStateIDLE int = iota
	bgpStateCONNECT
	bgpStateACTIVE
	bgpStateOPENSENT
	bgpStateOPENCONFIRM
	bgpStateESTABLISHED
)

// Peer holds all the configuration for a BGP Neighbor.
type Peer struct {
	Host     net.IP                 // allow connections from IPv6, but at present we only support IPv4 Unicast AFI/SAFI
	Port     uint16                 // this should default to 179
	LocalASN uint16                 // this is only used for our OPEN message, we're acting like a route-server
	HoldTime time.Duration          // truncated to seconds in the OPEN message
	RouterID uint32                 // must be unique, don't care what it is
	State    int                    // BGP FSM state, local significance only
	Updated  time.Time              // when the last update message was received, to prevent wasted effort later
	RIB      *critbitgo.Net         // radix tree, value is all BGP path attributes
	Map      map[string][]net.IPNet // map of NEXT_HOP to prefixes, for fast lookups later. string because net.IP is slice
	conn     net.Conn               // actual connection to our peer
	msgSend  chan []byte            // messages TO the peer
	msgRecv  chan []byte            // messages FROM the peer
}

func (peer Peer) sendMsg(msgType byte, msgBody []byte) {
	// work out our message length
	msgLength := make([]byte, 2)
	// 4096 == max message size -- no draft-ietf-idr-bgp-extended-messages here for now...
	// less 16 (BGP header)
	// less 2 (message length)
	// less 1 (message type)
	if len(msgBody) < (4096 - 16 - 2 - 1) {
		binary.BigEndian.PutUint16(msgLength, uint16(16+2+1+len(msgBody)))
	} else {
		log.Fatal("BGP MSG TOO BIG!")
	}

	// create the message by starting off with the daft BGPv4 header
	msg := []byte{255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255}

	msg = append(msg, msgLength...)
	msg = append(msg, msgType)
	msg = append(msg, msgBody...)

	peer.msgSend <- msg
}

// Dial establishes a connection to a BGP speaker, negotiates an OPEN, then sets up a keepalive.
func (peer *Peer) Dial() error {
	// create the message channels
	peer.msgSend = make(chan []byte)
	peer.msgRecv = make(chan []byte)
	shutdownChan := make(chan bool, 2)

	// we'll refer to this a lot, so build it now
	address := net.JoinHostPort(peer.Host.String(), strconv.Itoa(int(peer.Port)))

	// create a tcp connection to the address -- resolve if needed, tcp4/6 as necessary
	peer.State = bgpStateCONNECT
	var err error
	peer.conn, err = net.Dial("tcp", address)
	if err != nil {
		peer.State = bgpStateIDLE
		return errors.New("Failed to connect")
	}

	// create outbound message queue
	go func() {
		for {
			msg := <-peer.msgSend
			err := binary.Write(peer.conn, binary.BigEndian, msg)
			if err != nil {
				// sleep a few seconds before restarting
				peer.conn.Close()
				peer.State = bgpStateIDLE
				return
			}
		}
	}()

	// send open message
	var openMsg []byte

	// write BGP Version (uint8) direct to message
	openMsg = append(openMsg, bgpVersion)

	// convert LocalASN (uint16) into Network Byte Order, and append to message
	localASN := make([]byte, 2)
	binary.BigEndian.PutUint16(localASN, peer.LocalASN)
	openMsg = append(openMsg, localASN...)

	// convert HoldTime (uint16) into Network Byte Order, and append to message
	holdtime := make([]byte, 2)
	binary.BigEndian.PutUint16(holdtime, uint16(peer.HoldTime.Seconds()))
	openMsg = append(openMsg, holdtime...)

	// convert RouterID (uint32) into Network Byte Order, and append to message
	routerid := make([]byte, 4)
	binary.BigEndian.PutUint32(routerid, peer.RouterID)
	openMsg = append(openMsg, routerid...)

	// send optional parameter length (spelling from RFC4271)
	var optParmLen byte // zero
	openMsg = append(openMsg, optParmLen)

	// send the OPEN message
	peer.sendMsg(bgpTypeOPEN, openMsg)
	peer.State = bgpStateOPENSENT

	// read the open message in reply
	// probably ignore optional parameters incl. capabilities

	// check the marker
	bufOpenMarker := make([]byte, 16)
	io.ReadFull(peer.conn, bufOpenMarker)
	for _, m := range bufOpenMarker {
		if m != 255 {
			log.Printf("BAD MARKER! %v", bufOpenMarker)
			peer.sendMsg(bgpTypeNOTIFICATION, []byte{1, 1})
			peer.conn.Close()
			peer.State = bgpStateIDLE
			return errors.New("Bad Open")
		}
	}

	// read the length
	bufOpenLength := make([]byte, 2)
	io.ReadFull(peer.conn, bufOpenLength)
	openLength := binary.BigEndian.Uint16(bufOpenLength)
	if openLength > 4096 {
		log.Fatal("BGP MESSAGE TOO BIG!")
	} else if openLength < 19 {
		log.Fatal("BGP MESSAGE TOO SMALL!")
	}

	// read the type
	bufOpenType := make([]byte, 1)
	io.ReadFull(peer.conn, bufOpenType)
	if int(bufOpenType[0]) != 1 {
		log.Printf("FIRST RECEIVED MESSAGE SHOULD BE OPEN!")
		peer.sendMsg(bgpTypeNOTIFICATION, []byte{2, 0})
		peer.conn.Close()
		peer.State = bgpStateIDLE
		return errors.New("Bad Open")
	}

	// check the version
	bufOpenVersion := make([]byte, 1)
	io.ReadFull(peer.conn, bufOpenVersion)
	if int(bufOpenVersion[0]) != 4 {
		log.Printf("BAD VERSION! %v", bufOpenVersion)
		peer.sendMsg(bgpTypeNOTIFICATION, []byte{2, 1})
		peer.State = bgpStateIDLE
		peer.conn.Close()
		return errors.New("Bad Version")
	}

	// check the ASN is our local ASN (iBGP TTL = 1, so remote needs to be RR Client)
	bufOpenASN := make([]byte, 2)
	io.ReadFull(peer.conn, bufOpenASN)
	if binary.BigEndian.Uint16(bufOpenASN) != peer.LocalASN {
		log.Printf("BAD ASN! %v", binary.BigEndian.Uint16(bufOpenASN))
		peer.sendMsg(bgpTypeNOTIFICATION, []byte{2, 2})
		peer.State = bgpStateIDLE
		peer.conn.Close()
		return errors.New("Bad Remote ASN")
	}

	// check the HoldTime is acceptable
	bufOpenHoldTime := make([]byte, 2)
	io.ReadFull(peer.conn, bufOpenHoldTime)
	if binary.BigEndian.Uint16(bufOpenHoldTime) < 3 {
		log.Printf("BAD HOLD TIME! %v", binary.BigEndian.Uint16(bufOpenHoldTime))
		peer.sendMsg(bgpTypeNOTIFICATION, []byte{2, 6})
		peer.State = bgpStateIDLE
		peer.conn.Close()
		return errors.New("Bad Hold Time")
	} else if float64(binary.BigEndian.Uint16(bufOpenHoldTime)) < peer.HoldTime.Seconds() {
		// compare the hold times, overwrite config to use the lower value (min. 3)
		peer.HoldTime = time.Duration(binary.BigEndian.Uint16(bufOpenHoldTime)) * time.Second
	}

	// BGP Identifier... Whatever
	bufOpenRouterID := make([]byte, 4)
	io.ReadFull(peer.conn, bufOpenRouterID)
	// we actually don't care, so just ignore this value

	// Optional Parameters... Whatever
	bufOpen := make([]byte, 1)
	io.ReadFull(peer.conn, bufOpen)
	optParmLen = bufOpen[0]
	if optParmLen > 0 {
		bufOpen = make([]byte, optParmLen)
		io.ReadFull(peer.conn, bufOpen)
	}
	// we actually don't care, so just ignore the data

	// we've got a live OPEN message...
	peer.State = bgpStateOPENCONFIRM

	// start KEEPALIVE goroutine, sending every (negotiated) HoldTime/3
	go func() {
		for {
			peer.sendMsg(bgpTypeKEEPALIVE, nil)
			time.Sleep(peer.HoldTime / 3)
		}
	}()

	// we also have a timer that send a NOTIFICATION in the event of HoldTime expiry
	notifyTimer := time.NewTimer(peer.HoldTime)
	go func() {
		select {
		case <-shutdownChan:
		case <-notifyTimer.C:
			peer.sendMsg(bgpTypeNOTIFICATION, []byte{4, 0})
			peer.conn.Close()

			// none of our data is valid anymore clear everything out
			peer.RIB.Clear()
			peer.Map = make(map[string][]net.IPNet)
			peer.State = bgpStateIDLE
		}
	}()

	// create inbound message queue now that we have a working connection
	go func() {
		// provide an escape, closing the update channel along the way
		var quit bool
		defer func() { shutdownChan <- true; shutdownChan <- true }()

		for {
			// read 16 bytes off the connection
			bufMarker := make([]byte, 16)
			io.ReadFull(peer.conn, bufMarker)

			// check header marker, confirms synchronisation intact
			for _, m := range bufMarker {
				if m != 255 {
					// can't be a valid message, quit
					quit = true
				}
			}

			// if we didn't get a valid marker, quit now
			if quit == true {
				break
			}

			// read 2 bytes off the connection (Length)
			bufLength := make([]byte, 2)
			io.ReadFull(peer.conn, bufLength)

			// check if the length is a sane value
			bgpLength := binary.BigEndian.Uint16(bufLength)
			if bgpLength > 4096 {
				log.Fatal("BGP MESSAGE TOO BIG!")
			} else if bgpLength < 19 {
				log.Fatal("BGP MESSAGE TOO SMALL!")
			}

			// calculate remaining bytes left in message, if any
			var remaining = bgpLength - 16 - 2 - 1
			bufData := make([]byte, remaining)
			if remaining > 0 {
				// don't read if zero bytes are expected
				io.ReadFull(peer.conn, bufData)
			}

			// read 1 byte off the connection (Type)
			bufType := make([]byte, 1)
			io.ReadFull(peer.conn, bufType)

			// check if the type is a sane value
			switch bufType[0] {
			case 1:
				log.Fatal("BGP DUPLICATE OPEN MESSAGE!")
			case 2:
				// UPDATE
				peer.msgRecv <- bufData
				peer.Updated = time.Now()
				// RFC4271 says we can treat this as a KEEPALIVE message
				_ = notifyTimer.Reset(peer.HoldTime)
			case 3:
				// NOTIFICATION
				peer.conn.Close()

				// none of our data is valid anymore clear everything out
				peer.RIB.Clear()
				peer.Map = make(map[string][]net.IPNet)
				peer.State = bgpStateIDLE

				// force cleanup
				break
			case 4:
				// KEEPALIVE
				// Reset Keep-Alive timer, no further action necessary
				// We will not reach this point if the timer has expired, so disregard return value
				_ = notifyTimer.Reset(peer.HoldTime)
			case 5:
				// ROUTE-REFRESH
				// Send NOTIFICATION, we don't support this, and we didn't mark it as a CAPABILITY!
				peer.sendMsg(bgpTypeNOTIFICATION, []byte{5, 0})
				peer.conn.Close()

				// none of our data is valid anymore clear everything out
				peer.RIB.Clear()
				peer.Map = make(map[string][]net.IPNet)
				peer.State = bgpStateIDLE
			default:
				log.Fatal("BGP UNKNOWN MESSAGE TYPE!")
			}
		}
	}()

	// with a new BGP session up and ready, clear out existing RIB state if we forgot to earlier
	peer.RIB.Clear()
	peer.Map = make(map[string][]net.IPNet)

	// Fully established on the localizer, descend with the ILS
	peer.State = bgpStateESTABLISHED

	// process UPDATE messages
	go func() {
		for {
			select {
			case <-shutdownChan:
				return
			case msg := <-peer.msgRecv:
				// UPDATE messages are delivered on this channel, stripped of marker etc

				// how many prefixes to withdraw?
				var lenWithdraw []byte
				lenWithdraw, msg = msg[0:2], msg[2:]

				// while we have prefixes left to process
				var processed uint16
				lenWithdrawInt := binary.BigEndian.Uint16(lenWithdraw)
				for processed < lenWithdrawInt {
					var mask byte
					mask, msg = msg[0], msg[1:]
					processed++
					prefix := make([]byte, 4)
					switch {
					case mask == 0:
						// do nothing, route is 0.0.0.0/0
					case mask <= 8:
						prefix[0], msg = msg[0], msg[1:]
						processed++
					case mask <= 16:
						for i := 0; i < 2; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					case mask <= 24:
						for i := 0; i < 3; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					case mask <= 32:
						for i := 0; i < 4; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					default:
						log.Fatal("BGP RECEIVED INVALID MASK SIZE!")
					}

					// withdraw prefix from RIB
					// FIXME: returns (value interface{}, ok bool, err error) but cba right now.
					peer.RIB.Delete(&net.IPNet{IP: prefix, Mask: net.CIDRMask(int(mask), 32)})

					// withdraw prefix from Indexing Map
					peer.mapDelete(net.IPNet{IP: prefix, Mask: net.CIDRMask(int(mask), 32)})
				}

				// path attribute length
				var lenPathAttr []byte
				lenPathAttr, msg = msg[0:2], msg[2:]
				if binary.BigEndian.Uint16(lenPathAttr) == 0 {
					// no NLRIs follow
					continue
				}

				// store path attributes for use in the trie later
				pathAttrs := append(lenPathAttr, msg[0:binary.BigEndian.Uint16(lenPathAttr)]...)
				pathNextHop := make([]byte, 4)

				// process path attributes
				var processedPath uint16
				lenPathAttrInt := binary.BigEndian.Uint16(lenPathAttr)
				for processedPath < lenPathAttrInt {
					// process attribute flag for extended length
					var attrFlags byte
					attrFlags, msg = msg[0], msg[1:]
					processedPath++

					// get attribute type code
					var attrType byte
					attrType, msg = msg[0], msg[1:]
					processedPath++

					// process attribute length flag (ignore other flags)
					var attrLen int
					if attrFlags&1<<4 == 0 {
						var length byte
						length, msg = msg[0], msg[1:]
						processedPath++
						attrLen = int(length)
					} else {
						var length []byte
						length, msg = msg[0:2], msg[2:]
						processedPath += 2
						attrLen = int(binary.BigEndian.Uint16(length))
					}

					// only select attribute type "NEXT_HOP"
					if attrType == 3 {
						if attrLen == 4 {
							copy(pathNextHop, msg[0:4])
							msg = msg[4:]
							processedPath += uint16(attrLen)
						} else {
							log.Fatal("BGP INCORRECT ATTR LEN FOR NEXT_HOP!")
						}
					} else {
						// we don't care about this attribute, skip over it
						msg = msg[attrLen:]
						processedPath += uint16(attrLen)
						continue
					}
				}

				// updated processed count to show path attributes have been processed
				processed += processedPath

				// convert aspath into string
				pathInfo := pathAttrs

				// process new NLRIs
				for {
					var mask byte
					mask, msg = msg[0], msg[1:]
					processed++
					prefix := make([]byte, 4)
					switch {
					case mask == 0:
						// do nothing, route is 0.0.0.0/0
					case mask <= 8:
						prefix[0], msg = msg[0], msg[1:]
						processed++
					case mask <= 16:
						for i := 0; i < 2; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					case mask <= 24:
						for i := 0; i < 3; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					case mask <= 32:
						for i := 0; i < 4; i++ {
							prefix[i], msg = msg[0], msg[1:]
							processed++
						}
					default:
						log.Fatal("BGP RECEIVED INVALID MASK SIZE!")
					}

					// add prefix to RIB
					peer.RIB.Add(&net.IPNet{IP: prefix, Mask: net.CIDRMask(int(mask), 32)}, pathInfo)

					// add prefix to Indexing map
					peer.mapAdd(net.IPNet{IP: prefix, Mask: net.CIDRMask(int(mask), 32)}, pathNextHop)

					// if we've run out of message, stop processing
					if len(msg) == 0 {
						break
					}
				}
			}
		}
	}()

	// no errors
	return nil
}

func (peer Peer) mapAdd(prefix net.IPNet, nextHop net.IP) {
	peer.Map[nextHop.String()] = append(peer.Map[nextHop.String()], prefix)
}

func (peer Peer) mapDelete(item net.IPNet) {
	// we've got to iterate over all the keys to find that prefix...
OuterLoop:
	for nextHop, prefixes := range peer.Map {
		for index, prefix := range prefixes {
			if reflect.DeepEqual(prefix, item) {
				peer.Map[nextHop] = append(peer.Map[nextHop][:index], peer.Map[nextHop][index+1:]...)
				break OuterLoop
			}
		}
	}
}

// Withdraw will send a prefix withdrawal over a BGP session
func (peer Peer) Withdraw(prefix net.IPNet) error {
	// can't send if it's not up!
	if peer.State != bgpStateESTABLISHED {
		return errors.New("Prefix withdrawal not possible, BGP session is down")
	}

	// format withdrawal message
	msg := make([]byte, 0)
	ip := prefix.IP.To4()
	maskSize, _ := prefix.Mask.Size()
	switch {
	case maskSize == 0:
		msg = append(msg, []byte{0}...)
	case maskSize <= 8:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0])}...)
	case maskSize <= 16:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1])}...)
	case maskSize <= 24:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1]), byte(ip[2])}...)
	case maskSize <= 32:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1]), byte(ip[2]), byte(ip[3])}...)
	default:
		log.Fatal("BGP TRIED TO SEND INVALID MASK SIZE!")
	}

	// message starts with it's length
	msgLength := make([]byte, 2)
	binary.BigEndian.PutUint16(msgLength, uint16(len(msg)))
	msg = append(msgLength, msg...)
	peer.sendMsg(bgpTypeUPDATE, msg)
	return nil
}

// Announce will send a prefix announcement over a BGP session
func (peer Peer) Announce(prefix net.IPNet, nextHop net.IP) error {
	// can't send if it's not up!
	if peer.State != bgpStateESTABLISHED {
		return errors.New("Prefix announcement not possible, BGP session is down")
	}

	// format announce message
	msg := make([]byte, 0)
	msg = append(msg, []byte{0, 0}...)  // withdrawn routes length
	msg = append(msg, []byte{0, 14}...) // path attribute length

	// ORIGIN
	msg = append(msg, []byte{1 << 6}...) // well-known, transitive, complete, short
	msg = append(msg, []byte{1}...)      // ORIGIN
	msg = append(msg, []byte{1}...)      // ORIGIN: Length
	msg = append(msg, []byte{2}...)      // ORIGIN: INCOMPLETE

	// AS_PATH
	localASN := make([]byte, 2)
	binary.BigEndian.PutUint16(localASN, uint16(peer.LocalASN))
	msg = append(msg, []byte{1 << 6}...) // well-known, transitive, complete, short
	msg = append(msg, []byte{2}...)      // AS_PATH
	msg = append(msg, []byte{0}...)      // AS_PATH: Length (empty as iBGP and no AS_PATH so far)

	// NEXT_HOP
	ipNextHop := nextHop.To4()
	msg = append(msg, []byte{1 << 6}...)                                                 // well-known, transitive, complete, short
	msg = append(msg, []byte{3}...)                                                      // NEXT_HOP
	msg = append(msg, []byte{4}...)                                                      // NEXT_HOP: Length
	msg = append(msg, []byte{ipNextHop[0], ipNextHop[1], ipNextHop[2], ipNextHop[3]}...) // NEXT_HOP IP

	ip := prefix.IP.To4()
	maskSize, _ := prefix.Mask.Size()
	switch {
	case maskSize == 0:
		msg = append(msg, []byte{0}...)
	case maskSize <= 8:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0])}...)
	case maskSize <= 16:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1])}...)
	case maskSize <= 24:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1]), byte(ip[2])}...)
	case maskSize <= 32:
		msg = append(msg, []byte{byte(maskSize)}...)
		msg = append(msg, []byte{byte(ip[0]), byte(ip[1]), byte(ip[2]), byte(ip[3])}...)
	default:
		log.Fatal("BGP TRIED TO SEND INVALID MASK SIZE!")
	}

	peer.sendMsg(bgpTypeUPDATE, msg)
	return nil
}
