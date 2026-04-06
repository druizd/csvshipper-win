package main

import (
	"context"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/google/uuid"
)

type RabbitClient struct {
	conn       *amqp.Connection
	ch         *amqp.Channel
	replyQueue amqp.Queue
	msgs       <-chan amqp.Delivery
}

func ConnectRabbit(cfg *Config) (*RabbitClient, error) {
	conn, err := amqp.Dial(cfg.RabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open a channel: %w", err)
	}

	// Declare an anonymous exclusive queue for replies
	q, err := ch.QueueDeclare(
		"",    // name - let RabbitMQ generate it
		false, // durable
		false, // delete when unused
		true,  // exclusive
		false, // noWait
		nil,   // arguments
	)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to declare a reply queue: %w", err)
	}

	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		true,   // auto-ack
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to register a consumer: %w", err)
	}

	return &RabbitClient{
		conn:       conn,
		ch:         ch,
		replyQueue: q,
		msgs:       msgs,
	}, nil
}

func (rc *RabbitClient) Close() {
	if rc.ch != nil {
		rc.ch.Close()
	}
	if rc.conn != nil {
		rc.conn.Close()
	}
}

// ExecuteSQLRPC sends the SQL to the target queue and waits for a response.
// Returns nil on success, or an error if failed or timeout.
func (rc *RabbitClient) ExecuteSQLRPC(ctx context.Context, targetQueue string, sqlContent string, timeoutSeconds int) error {
	corrId := uuid.NewString()

	err := rc.ch.PublishWithContext(ctx,
		"",          // exchange
		targetQueue, // routing key
		false,       // mandatory
		false,       // immediate
		amqp.Publishing{
			ContentType:   "text/plain",
			CorrelationId: corrId,
			ReplyTo:       rc.replyQueue.Name,
			Body:          []byte(sqlContent),
		})
	if err != nil {
		return fmt.Errorf("failed to publish a message: %w", err)
	}

	timeout := time.After(time.Duration(timeoutSeconds) * time.Second)

	for {
		select {
		case d := <-rc.msgs:
			if d.CorrelationId == corrId {
				// We expect the consumer to reply with "SUCCESS" or "ERROR:..."
				res := string(d.Body)
				if res == "SUCCESS" {
					return nil
				}
				return fmt.Errorf("remote execution failed: %s", res)
			}
		case <-timeout:
			return fmt.Errorf("rpc timeout after %d seconds", timeoutSeconds)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SendHeartbeat continuously sends a heartbeat message to keep the linux API updated
func (rc *RabbitClient) SendHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Just publish a simple ping to status_queue
			err := rc.ch.PublishWithContext(ctx,
				"",             // exchange
				"status_queue", // routing key
				false,          // mandatory
				false,          // immediate
				amqp.Publishing{
					ContentType: "application/json",
					Body:        []byte(`{"os":"windows","status":"UP"}`),
				})
			if err != nil {
				log.Printf("Failed to send heartbeat: %v", err)
			}
		}
	}
}
