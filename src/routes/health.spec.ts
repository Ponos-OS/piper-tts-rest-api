import { jest } from "@jest/globals";

import { handleHealth } from "./health.js";

describe(handleHealth.name, () => {
  it("should send OK response", () => {
    const res = {
      end: jest.fn(),
      writeHead: jest.fn(),
    } as any;

    handleHealth(res);

    expect(res.writeHead).toHaveBeenCalledWith(200, {
      "Content-Type": "text/plain; charset=utf-8",
    });
    expect(res.end).toHaveBeenCalledWith("OK");
  });
});
