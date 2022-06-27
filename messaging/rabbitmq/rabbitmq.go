package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/streadway/amqp"

	"github.com/ralvescostati/pkgs/env"
	"github.com/ralvescostati/pkgs/logger"
	"github.com/ralvescostati/pkgs/messaging"
)

type ExchangeKind string

const (
	DIRECT_EXCHANGE  ExchangeKind = "direct"
	FANOUT_EXCHANGE  ExchangeKind = "fanout"
	TOPIC_EXCHANGE   ExchangeKind = "topic"
	HEADERS_EXCHANGE ExchangeKind = "headers"
	DELAY_EXCHANGE   ExchangeKind = "delay"

	ConnErrorMessage    = "[RabbitMQ::Connect] failure to connect to the %s: %s"
	DeclareErrorMessage = "[RabbitMQ::Connect] failure to declare %s: %s"
	BindErrorMessage    = "[RabbitMQ::Connect] failure to bind %s: %s"
)

type (
	// Params is a RabbitMQ params needed to declare an Exchange, Queue or Bind them
	Params struct {
		ExchangeName     string
		ExchangeType     ExchangeKind
		QueueName        string
		RoutingKey       string
		Retryable        bool
		EnabledTelemetry bool
	}

	// IRabbitMQMessaging is RabbitMQ Config Builder
	IRabbitMQMessaging interface {
		AssertExchange(params *Params) IRabbitMQMessaging
		AssertQueue(params *Params) IRabbitMQMessaging
		Binding(params *Params) IRabbitMQMessaging
		AssertExchangeWithDeadLetter() IRabbitMQMessaging
		Build() (messaging.IMessageBroker[Params], error)
	}

	// AMQPChannel is an abstraction for AMQP default channel to improve unit tests
	AMQPChannel interface {
		ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
		QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
		QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
		Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	}

	Dispatcher struct {
		Queue          string
		ReceiveMsgType string
		ReflectedType  reflect.Value
		Handler        messaging.Handler
	}

	// IRabbitMQMessaging is the implementation for IRabbitMQMessaging
	RabbitMQMessaging struct {
		Err         error
		logger      logger.ILogger
		conn        *amqp.Connection
		ch          AMQPChannel
		dispatchers map[string][]*Dispatcher
	}
)

// New(...) create a new instance for IRabbitMQMessaging
//
// New(...) connect to the RabbitMQ broker and stablish a channel
func New(cfg *env.Configs, logger logger.ILogger) IRabbitMQMessaging {
	rb := &RabbitMQMessaging{
		logger:      logger,
		dispatchers: map[string][]*Dispatcher{},
	}

	conn, err := amqp.Dial(fmt.Sprintf("amqp://%s:%s@%s:%s", cfg.RABBIT_USER, cfg.RABBIT_PASSWORD, cfg.RABBIT_VHOST, cfg.RABBIT_PORT))
	if err != nil {
		logger.Error(fmt.Sprintf(ConnErrorMessage, "broker", err))
		rb.Err = err
		return rb
	}

	rb.conn = conn
	ch, err := conn.Channel()
	if err != nil {
		logger.Error(fmt.Sprintf(ConnErrorMessage, "channel", err))
		rb.Err = err
		return rb
	}

	rb.ch = ch

	return rb
}

// AssertExchange Declare a durable, not excluded Exchange with the following parameters
func (m *RabbitMQMessaging) AssertExchange(params *Params) IRabbitMQMessaging {
	if m.Err != nil {
		return m
	}

	err := m.ch.ExchangeDeclare(params.ExchangeName, string(params.ExchangeType), true, false, false, false, nil)
	if err != nil {
		m.Err = err
		m.logger.Error(fmt.Sprintf(DeclareErrorMessage, "exchange", err))
		return m
	}

	return m
}

// AssertExchangeAssertQueue Declare a durable, not excluded Queue with the following parameters
func (m *RabbitMQMessaging) AssertQueue(params *Params) IRabbitMQMessaging {
	if m.Err != nil {
		return m
	}

	_, err := m.ch.QueueDeclare(params.QueueName, true, false, false, false, nil)
	if err != nil {
		m.Err = err
		m.logger.Error(fmt.Sprintf(DeclareErrorMessage, "queue", err))
		return m
	}

	return m
}

// Binding bind an exchange/queue with the following parameters without extra RabbitMQ configurations such as Dead Letter.
func (m *RabbitMQMessaging) Binding(params *Params) IRabbitMQMessaging {
	if m.Err != nil {
		return m
	}

	err := m.ch.QueueBind(params.QueueName, params.RoutingKey, params.ExchangeName, false, nil)
	if err != nil {
		m.Err = err
		m.logger.Error(fmt.Sprintf(BindErrorMessage, "queue", err))
		return m
	}

	return m
}

// AssertExchange Declare a durable, not excluded Exchange with the following parameters with a default Dead Letter exchange
func (m *RabbitMQMessaging) AssertExchangeWithDeadLetter() IRabbitMQMessaging {
	if m.Err != nil {
		return m
	}

	return m
}

// AssertDelayedExchange will be declare a Delay exchange and configure a dead letter exchange and queue.
//
// When messages for delay exchange was noAck these messages will sent to the dead letter exchange/queue.
func (m *RabbitMQMessaging) AssertDelayedExchange() IRabbitMQMessaging {

	return m
}

func (m *RabbitMQMessaging) Build() (messaging.IMessageBroker[Params], error) {
	if m.Err != nil {
		return nil, m.Err
	}

	return m, nil
}

func (m *RabbitMQMessaging) Publisher(ctx context.Context, params *Params, msg any, opts map[string]any) error {
	return nil
}

// AddDispatcher Add the handler and msg type
//
// Each time a message came, we check the queue, and get the available handlers for that queue.
// After we do a coercion of the msg type to check which handler expect this msg type
func (m *RabbitMQMessaging) AddDispatcher(queue string, handler messaging.Handler, receiveMsgType any) error {
	if receiveMsgType == nil || queue == "" {
		return errors.New("[RabbitMQ:AddDispatcher]")
	}

	h, ok := m.dispatchers[queue]
	if !ok {
		m.dispatchers[queue] = []*Dispatcher{
			{
				Queue:          queue,
				Handler:        handler,
				ReceiveMsgType: fmt.Sprintf("%T", receiveMsgType),
				ReflectedType:  reflect.New(reflect.TypeOf(receiveMsgType).Elem()),
			},
		}
	}

	m.dispatchers[queue] = append(h, &Dispatcher{
		Queue:          queue,
		Handler:        handler,
		ReceiveMsgType: fmt.Sprintf("%T", receiveMsgType),
		ReflectedType:  reflect.New(reflect.TypeOf(receiveMsgType).Elem()),
	})

	return nil
}

func (m *RabbitMQMessaging) Subscriber(ctx context.Context, params *Params) error {
	delivery, err := m.ch.Consume(params.QueueName, params.RoutingKey, false, false, false, false, nil)
	if err != nil {
		return err
	}

	go m.exec(params, delivery)

	return nil
}

func (m *RabbitMQMessaging) exec(params *Params, delivery <-chan amqp.Delivery) {
	for received := range delivery {
		msgType, ok := received.Headers["type"].(string)
		if !ok {
			m.logger.Warn("[RabbitMQ:HandlerExecutor] ignore message reason: message without type header")
			received.Ack(true)
			continue
		}

		dispatchers, ok := m.dispatchers[params.QueueName]
		if !ok {
			m.logger.Warn("[RabbitMQ:HandlerExecutor] ignore message reason: there is no handler for this queue registered yet")
			received.Ack(true)
			continue
		}

		var mPointer any
		var handler messaging.Handler

		for _, d := range dispatchers {
			if d.ReceiveMsgType == msgType {
				mPointer = d.ReflectedType.Interface()

				err := json.Unmarshal(received.Body, mPointer)
				if err == nil {
					handler = d.Handler
					break
				}
			}
		}

		if mPointer == nil || handler == nil {
			m.logger.Error(fmt.Sprintf("[RabbitMQ:HandlerExecutor] ignore message reason: failure type coercion. Queue: %s.", params.QueueName))
			received.Ack(true)
			continue
		}

		m.logger.Info(fmt.Sprintf("[RabbitMQ:HandlerExecutor] message received %T", mPointer))

		err := handler(mPointer, nil)
		if err == nil {
			m.logger.Info("[RabbitMQ:HandlerExecutor] message properly processed")
			received.Ack(true)
			continue
		}

		m.logger.Error(err.Error())

		if !params.Retryable {
			m.logger.Warn("[RabbitMQ:HandlerExecutor] message has no retry police, purging message")
			received.Ack(true)
			continue
		}

		m.logger.Debug("[RabbitMQ:HandlerExecutor] sending failure msg to delayed exchange")
		m.Publisher(context.Background(), nil, nil, nil)

		received.Ack(true)
	}
}
