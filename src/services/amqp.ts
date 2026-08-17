import type {
  ChannelModel,
  ConfirmChannel,
  ConsumeMessage,
  Options,
} from "amqplib";

import { connect } from "amqplib";

import type { SynthesizeConfig } from "../shared/index.js";

const SYNTHESIZE_ROUTING_KEY = "synthesize";
const QUORUM_DELIVERY_LIMIT = 5;

export async function connectAmqpBroker(
  rabbitmq: SynthesizeConfig["rabbitmq"],
  client: AmqpClient = { connect },
): Promise<AmqpBroker> {
  const connection = await client.connect(rabbitmq.url);

  try {
    const channel = await connection.createConfirmChannel();
    const broker = new ConfirmedAmqpBroker(connection, channel, rabbitmq);

    await broker.initialize();

    return broker;
  } catch (error) {
    await connection.close();
    throw error;
  }
}

/**
 * @description for now this is not extendible & useable for other exchanges -- hard coded exchange type and  are using synthesize RabbitMQ URL. I am even not sure if we would be needing to connect to different RabbitMQ instances.
 */
class ConfirmedAmqpBroker implements AmqpBroker {
  readonly #connection: Pick<ChannelModel, "close">;
  readonly #channel: ConfirmChannel;
  readonly #rabbitmq: SynthesizeConfig["rabbitmq"];
  readonly #consumerTags = new Set<string>();
  #closed = false;

  constructor(
    connection: Pick<ChannelModel, "close">,
    channel: ConfirmChannel,
    rabbitmq: SynthesizeConfig["rabbitmq"],
  ) {
    this.#connection = connection;
    this.#channel = channel;
    this.#rabbitmq = rabbitmq;
  }

  async initialize(): Promise<void> {
    await this.#channel.assertExchange(this.#rabbitmq.exchange, "direct", {
      durable: true,
    });
    await this.#channel.assertExchange(
      this.#rabbitmq.deadLetterExchange,
      "direct",
      { durable: true },
    );
    await this.#channel.assertQueue(this.#rabbitmq.deadLetterQueue, {
      durable: true,
      arguments: { "x-queue-type": "quorum" },
    });
    await this.#channel.bindQueue(
      this.#rabbitmq.deadLetterQueue,
      this.#rabbitmq.deadLetterExchange,
      SYNTHESIZE_ROUTING_KEY,
    );
    await this.#channel.assertQueue(this.#rabbitmq.workQueue, {
      durable: true,
      arguments: {
        "x-queue-type": "quorum",
        "x-delivery-limit": QUORUM_DELIVERY_LIMIT,
        "x-dead-letter-exchange": this.#rabbitmq.deadLetterExchange,
        "x-dead-letter-routing-key": SYNTHESIZE_ROUTING_KEY,
      },
    });
    await this.#channel.bindQueue(
      this.#rabbitmq.workQueue,
      this.#rabbitmq.exchange,
      SYNTHESIZE_ROUTING_KEY,
    );
    await this.#channel.assertQueue(this.#rabbitmq.retryQueue, {
      durable: true,
      arguments: {
        "x-queue-type": "quorum",
        "x-dead-letter-exchange": this.#rabbitmq.exchange,
        "x-dead-letter-routing-key": SYNTHESIZE_ROUTING_KEY,
      },
    });
    await this.#channel.prefetch(1);
  }

  async publish(message: AmqpPublishMessage): Promise<void> {
    this.#channel.publish(
      message.exchange,
      message.routingKey,
      message.content,
      message.options,
    );
    await this.#channel.waitForConfirms();
  }

  async consume(
    onMessage: (message: ConsumeMessage | null) => void,
  ): Promise<string> {
    const { consumerTag } = await this.#channel.consume(
      this.#rabbitmq.workQueue,
      onMessage,
      { noAck: false },
    );
    this.#consumerTags.add(consumerTag);
    return consumerTag;
  }

  acknowledge(message: ConsumeMessage): void {
    this.#channel.ack(message);
  }

  async close(): Promise<void> {
    if (this.#closed) {
      return;
    }

    this.#closed = true;

    await Promise.allSettled(
      [...this.#consumerTags].map((consumerTag) =>
        this.#channel.cancel(consumerTag),
      ),
    );

    this.#consumerTags.clear();

    await this.#channel.close();
    await this.#connection.close();
  }
}

export interface AmqpClient {
  connect(
    url: string,
  ): Promise<Pick<ChannelModel, "close" | "createConfirmChannel">>;
}

export interface AmqpBroker {
  publish(message: AmqpPublishMessage): Promise<void>;
  consume(onMessage: (message: ConsumeMessage | null) => void): Promise<string>;
  acknowledge(message: ConsumeMessage): void;
  close(): Promise<void>;
}

export interface AmqpPublishMessage {
  exchange: string;
  routingKey: string;
  content: Buffer;
  options?: Options.Publish;
}
