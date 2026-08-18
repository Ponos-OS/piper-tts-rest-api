import { type Channel, connect, type GetMessage } from "amqplib";

describe("POST /synthesize", () => {
  beforeEach(async () => {
    const connection = await connect("amqp://localhost:5672");
    const channel = await connection.createChannel();
    await channel.purgeQueue("synthesize.work");
    await channel.close();
    await connection.close();
  });

  it("should accept a real HTTP request & durably publishes its job to RabbitMQ", async () => {
    const response = await fetch("http://localhost:3000/synthesize", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        text: "Hello from the HTTP integration test",
        genUploadUrl: "https://callbacks.example.com/upload-url",
        statusCallbackUrl: "https://callbacks.example.com/status",
      }),
    });
    const payload = (await response.json()) as { jobId: string };
    const message = await getMessage();

    expect(response.status).toBe(202);
    expect(payload.jobId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    );
    expect(message.properties.messageId).toBe(payload.jobId);
    expect(message.properties.deliveryMode).toBe(2);
    expect(JSON.parse(message.content.toString("utf8"))).toEqual({
      jobId: payload.jobId,
      text: "Hello from the HTTP integration test",
      genUploadUrl: "https://callbacks.example.com/upload-url",
      statusCallbackUrl: "https://callbacks.example.com/status",
    });
  });
});

async function getMessage(): Promise<GetMessage> {
  const connection = await connect("amqp://localhost:5672");
  const channel = await connection.createChannel();
  const message = await waitForMessage(channel, "synthesize.work");
  channel.ack(message);
  await channel.close();
  await connection.close();
  return message;
}

async function waitForMessage(
  channel: Channel,
  queue: string,
): Promise<GetMessage> {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    const message = await channel.get(queue, { noAck: false });
    if (message !== false) {
      return message;
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error("Timed out waiting for a published synthesize job");
}
