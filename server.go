package main

import (
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"time"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Client is a middleman between the websocket connection and the subscription.
type Client struct {
	name string
	conn *websocket.Conn
	send chan []byte
}

type PubOrSub int8

const (
	unsub PubOrSub = iota - 1
	pub
	sub
)

type message struct {
	Action  PubOrSub    `json:"action"`
	Topic   string      `json:"topic"`
	Payload interface{} `json:"payload"`
}

// readPump pumps messages from the websocket connection to the subscription.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump(s *Subscription) {
	defer func() {
		_ = c.conn.Close()
	}()
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		var wsMsg message
		err := c.conn.ReadJSON(&wsMsg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseMessage, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("closed client error: %v\n", err)
				s.RemoveClient(c)
				break
			}
			if websocket.IsCloseError(err, websocket.CloseMessage, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("%s> leaving\n", c.name)
				s.RemoveClient(c)
				break
			}
			log.Printf("%s> error reading message: %v\n", c.name, err)
		}
		switch wsMsg.Action {
		case sub:
			log.Printf("%s> subscribing - %s \n", c.name, wsMsg.Topic)
			s.Subscribe(wsMsg.Topic, c)
			break
		case pub:
			log.Printf("%s> publishing - %s \n", c.name, wsMsg.Topic)
			s.Publish(wsMsg.Topic, wsMsg)
			break
		case unsub:
			log.Printf("%s> unsubscribing - %s \n", c.name, wsMsg.Topic)
			s.UnSubscribe(wsMsg.Topic, c)
			break
		default:
			log.Printf("%s> unknown action %s\n", c.name, wsMsg.Topic)
			break
		}
	}
}

// writePump pumps messages from the subscription to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump(s *Subscription) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		s.RemoveClient(c)
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))

			if !ok {
				// The subscription closed the channel.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, _ = w.Write(msg)

			// Add queued messages to the current websocket message.
			n := len(c.send)
			for i := 0; i < n; i++ {
				_, _ = w.Write(<-c.send)
			}

			if err = w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			err := c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err != nil {
				return
			}
			if err = c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs handles websocket requests from the peer.
func serveWs(subscription *Subscription, w http.ResponseWriter, r *http.Request) {
	defer log.Println("new client ", r.URL.Query().Get("client-name"))
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{
		name: r.URL.Query().Get("client-name"),
		conn: conn,
		send: make(chan []byte, 256),
	}

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	r.Header.Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
	go client.writePump(subscription)
	go client.readPump(subscription)
}
