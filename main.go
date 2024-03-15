package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
)

var NATS_URL string

func init() {
	NATS_URL = os.Getenv("NATS_URL")
	if NATS_URL == "" {
		NATS_URL = nats.DefaultURL
	}
}

func main() {
	nc, err := nats.Connect(
		NATS_URL,
		nats.ErrorHandler(func(nc *nats.Conn, s *nats.Subscription, err error) {
			if s != nil {
				log.Printf("Async error in %q/%q: %v", s.Subject, s.Queue, err)
			} else {
				log.Printf("Async error outside subscription: %v", err)
			}
		}),
	)
	if err != nil {
		panic(err)
	}
	defer nc.Close()

	app := gin.Default()

	apiRouter := app.Group("/api")

	service := NewService(nc)
	service.Bind(apiRouter)

	if err := app.Run("127.0.0.1:8080"); err != nil {
		panic(err)
	}
}

type Service struct {
	nc  *nats.Conn
	nec *nats.EncodedConn
}

func NewService(nc *nats.Conn) *Service {
	nec, err := nats.NewEncodedConn(nc, nats.JSON_ENCODER)
	if err != nil {
		panic(err)
	}
	return &Service{
		nc:  nc,
		nec: nec,
	}
}

func (s *Service) Bind(router *gin.RouterGroup) {
	router.GET("/sse/:id", s.SSEHandler)
	router.POST("/action/:id", s.ActionHandler)
}

func (s *Service) SSEHandler(c *gin.Context) {
	id := strings.ToLower(c.Param("id"))
	topic := TopicForUser(id)

	messages := make(chan *ActionPayload, 10)

	subscriber, err := s.nec.Subscribe(topic, func(m *ActionPayload) {
		messages <- m
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer subscriber.Unsubscribe()

	messages <- &ActionPayload{
		Event: "timestamp",
		Data: map[string]interface{}{
			"timestamp": time.Now(),
			"topic":     topic,
		},
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	ctx, cancel := context.WithDeadline(c, time.Now().Add(30*time.Minute))
	defer cancel()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-c.Writer.CloseNotify():
			return false
		case msg := <-messages:
			c.SSEvent(msg.Event, msg.Data)
			return true
		}
	})
}

type ActionPayload struct {
	Event string      `json:"event" binding:"required"`
	Data  interface{} `json:"data"  binding:"required"`
}

func (s *Service) ActionHandler(c *gin.Context) {
	id := strings.ToLower(c.Param("id"))

	var payload ActionPayload
	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	topic := TopicForUser(id)

	if err := s.nec.Publish(topic, payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func TopicForUser(id string) string {
	return fmt.Sprintf("user.%s", id)
}
