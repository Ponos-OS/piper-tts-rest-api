import type { ChannelModel, ConfirmChannel } from "amqplib";

import { jest } from "@jest/globals";

import type { AmqpClient } from "./amqp.js";

import { connectAmqpBroker } from "./amqp.js";

const rabbitmq = {
  url: "amqp://rabbitmq:5672",
  exchange: "synthesize",
  workQueue: "synthesize.work",
  retryQueue: "synthesize.retry",
  deadLetterExchange: "synthesize.dlx",
  deadLetterQueue: "synthesize.dlq",
};

function createClient(): {
  client: AmqpClient;
  channel: ConfirmChannel;
  connection: Pick<ChannelModel, "close" | "createConfirmChannel">;
} {
  const channel = {
    assertExchange: jest.fn().mockResolvedValue({} as never),
    assertQueue: jest.fn().mockResolvedValue({} as never),
    bindQueue: jest.fn().mockResolvedValue({} as never),
    prefetch: jest.fn().mockResolvedValue({} as never),
    publish: jest.fn().mockReturnValue(true),
    waitForConfirms: jest.fn().mockResolvedValue(undefined as never),
    consume: jest
      .fn()
      .mockResolvedValue({ consumerTag: "consumer-1" } as never),
    cancel: jest.fn().mockResolvedValue({} as never),
    close: jest.fn().mockResolvedValue(undefined as never),
  } as any;
  const connection = {
    createConfirmChannel: jest.fn().mockResolvedValue(channel as never),
    close: jest.fn().mockResolvedValue(undefined as never),
  } as any;
  const client = {
    connect: jest.fn().mockResolvedValue(connection as never),
  } as any;

  return {
    client,
    channel,
    connection,
  };
}

describe(connectAmqpBroker.name, () => {
  it("declares durable quorum topology and prefetches one message", async () => {
    const { client, channel } = createClient();

    await connectAmqpBroker(rabbitmq, client);

    expect(channel.assertExchange).toHaveBeenCalledWith(
      "synthesize",
      "direct",
      {
        durable: true,
      },
    );
    expect(channel.assertExchange).toHaveBeenCalledWith(
      "synthesize.dlx",
      "direct",
      { durable: true },
    );
    expect(channel.assertQueue).toHaveBeenCalledWith("synthesize.work", {
      durable: true,
      arguments: {
        "x-queue-type": "quorum",
        "x-delivery-limit": 5,
        "x-dead-letter-exchange": "synthesize.dlx",
        "x-dead-letter-routing-key": "synthesize",
      },
    });
    expect(channel.assertQueue).toHaveBeenCalledWith("synthesize.retry", {
      durable: true,
      arguments: {
        "x-queue-type": "quorum",
        "x-dead-letter-exchange": "synthesize",
        "x-dead-letter-routing-key": "synthesize",
      },
    });
    expect(channel.assertQueue).toHaveBeenCalledWith("synthesize.dlq", {
      durable: true,
      arguments: { "x-queue-type": "quorum" },
    });
    expect(channel.bindQueue).toHaveBeenCalledWith(
      "synthesize.work",
      "synthesize",
      "synthesize",
    );
    expect(channel.bindQueue).toHaveBeenCalledWith(
      "synthesize.dlq",
      "synthesize.dlx",
      "synthesize",
    );
    expect(channel.prefetch).toHaveBeenCalledWith(1);
  });

  it("waits for the broker to confirm every publish", async () => {
    const { client, channel } = createClient();
    const broker = await connectAmqpBroker(rabbitmq, client);
    const content = Buffer.from('{"jobId":"job-1"}');

    await broker.publish({
      exchange: "synthesize",
      routingKey: "synthesize",
      content,
      options: { persistent: true, messageId: "job-1" },
    });

    expect(channel.publish).toHaveBeenCalledWith(
      "synthesize",
      "synthesize",
      content,
      { persistent: true, messageId: "job-1" },
    );
    expect(channel.waitForConfirms).toHaveBeenCalledTimes(1);
  });

  it("cancels active consumers before closing the channel and connection", async () => {
    const { client, channel, connection } = createClient();
    const broker = await connectAmqpBroker(rabbitmq, client);

    const consumerTag = await broker.consume(() => {});
    await broker.close();
    await broker.close();

    expect(consumerTag).toBe("consumer-1");
    expect(channel.consume).toHaveBeenCalledWith(
      "synthesize.work",
      expect.any(Function),
      { noAck: false },
    );
    expect(channel.cancel).toHaveBeenCalledWith("consumer-1");
    expect(channel.close).toHaveBeenCalledTimes(1);
    expect(connection.close).toHaveBeenCalledTimes(1);
  });

  it("closes the connection when topology declaration fails", async () => {
    const { client, channel, connection } = createClient();
    jest
      .mocked(channel.assertExchange)
      .mockRejectedValueOnce(new Error("broker unavailable"));

    await expect(connectAmqpBroker(rabbitmq, client)).rejects.toThrow(
      "broker unavailable",
    );

    expect(connection.close).toHaveBeenCalledTimes(1);
  });
});
