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

func (m *RabbitMQMessaging) AssertExchangeWithDeadLetter() IRabbitMQMessaging {
	if m.Err != nil {
		return m
	}

	return m
}

func (m *RabbitMQMessaging) AssertDelayedExchange() IRabbitMQMessaging {
	return m
}

func (m *RabbitMQMessaging) Build() (IRabbitMQMessaging, error) {
	return m, m.Err
}

func (m *RabbitMQMessaging) Publisher(ctx context.Context, params *Params, msg any, opts ...PublishOpts) error {
	return nil
}

func (m *RabbitMQMessaging) AddDispatcher(queue string, handler SubHandler, structWillUseToTypeCoercion any) error {
	if structWillUseToTypeCoercion == nil || queue == "" {
		return errors.New("[RabbitMQ:AddDispatcher]")
	}

	dispatch := &Dispatcher{
		Queue:          queue,
		Handler:        handler,
		ReceiveMsgType: fmt.Sprintf("%T", structWillUseToTypeCoercion),
		ReflectedType:  reflect.New(reflect.TypeOf(structWillUseToTypeCoercion).Elem()),
	}

	h, ok := m.dispatchers[queue]
	if !ok {
		m.dispatchers[queue] = []*Dispatcher{dispatch}
		return nil
	}

	m.dispatchers[queue] = append(h, dispatch)
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
		var handler SubHandler

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
		m.Publisher(context.Background(), nil, nil)

		received.Ack(true)
	}
}