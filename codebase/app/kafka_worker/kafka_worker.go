package kafkaworker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Shopify/sarama"
	"pkg.agungdwiprasetyo.com/candi/codebase/factory"
	"pkg.agungdwiprasetyo.com/candi/codebase/factory/types"
	"pkg.agungdwiprasetyo.com/candi/config/env"
	"pkg.agungdwiprasetyo.com/candi/logger"
)

type kafkaWorker struct {
	engine          sarama.ConsumerGroup
	service         factory.ServiceFactory
	consumerHandler *consumerHandler
	cancelFunc      func()
}

// NewWorker create new kafka consumer
func NewWorker(service factory.ServiceFactory) factory.AppServerFactory {
	// init kafka consumer
	if service.GetDependency().GetBroker().GetKafkaClient() == nil {
		log.Panic("Missing kafka configuration")
	}
	consumerEngine, err := sarama.NewConsumerGroupFromClient(
		env.BaseEnv().Kafka.ConsumerGroup,
		service.GetDependency().GetBroker().GetKafkaClient(),
	)
	if err != nil {
		log.Panicf("Error creating kafka consumer group client: %v", err)
	}

	var consumerHandler consumerHandler
	consumerHandler.handlerFuncs = make(map[string]struct {
		handlerFunc   types.WorkerHandlerFunc
		errorHandlers []types.WorkerErrorHandler
	})
	for _, m := range service.GetModules() {
		if h := m.WorkerHandler(types.Kafka); h != nil {
			var handlerGroup types.WorkerHandlerGroup
			h.MountHandlers(&handlerGroup)
			for _, handler := range handlerGroup.Handlers {
				if _, ok := consumerHandler.handlerFuncs[handler.Pattern]; ok {
					logger.LogYellow(fmt.Sprintf("Kafka: warning, topic %s has been used in another module, overwrite handler func", handler.Pattern))
				}
				consumerHandler.handlerFuncs[handler.Pattern] = struct {
					handlerFunc   types.WorkerHandlerFunc
					errorHandlers []types.WorkerErrorHandler
				}{
					handlerFunc: handler.HandlerFunc, errorHandlers: handler.ErrorHandler,
				}
				consumerHandler.topics = append(consumerHandler.topics, handler.Pattern)
				logger.LogYellow(fmt.Sprintf(`[KAFKA-CONSUMER] (topic): %-15s  --> (module): "%s"`, `"`+handler.Pattern+`"`, m.Name()))
			}
		}
	}
	fmt.Printf("\x1b[34;1m⇨ Kafka consumer running with %d topics. Brokers: "+strings.Join(env.BaseEnv().Kafka.Brokers, ", ")+"\x1b[0m\n\n",
		len(consumerHandler.topics))

	consumerHandler.semaphore = make(chan struct{}, env.BaseEnv().MaxGoroutines)
	consumerHandler.ready = make(chan struct{})
	return &kafkaWorker{
		engine:          consumerEngine,
		service:         service,
		consumerHandler: &consumerHandler,
	}
}

func (h *kafkaWorker) Serve() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancelFunc = cancel

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if err := h.engine.Consume(ctx, h.consumerHandler.topics, h.consumerHandler); err != nil {
				log.Printf("Error from kafka consumer: %v", err)
			}

			if ctx.Err() != nil {
				return
			}
			h.consumerHandler.ready = make(chan struct{})
		}
	}()

	<-h.consumerHandler.ready
	wg.Wait()
}

func (h *kafkaWorker) Shutdown(ctx context.Context) {
	log.Println("\x1b[33;1mStopping Kafka Consumer worker...\x1b[0m")
	defer func() { log.Println("\x1b[33;1mStopping Kafka Consumer:\x1b[0m \x1b[32;1mSUCCESS\x1b[0m") }()

	h.cancelFunc()
	h.engine.Close()
}
