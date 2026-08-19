import { wordCounter } from "./word-counter.util.js";

describe(wordCounter.name, () => {
  it("should count words", () => {
    const count = wordCounter(
      "Hi, this is ME!\n\n Do you know my name?\t\t I'm Kasir\n\n At your service!\n",
    );

    expect(count).toBe(14);
  });

  it.each(["", "\t\t", "\n\n", "\t\n"])("should return 0 for %j", (text) => {
    const count = wordCounter(text);

    expect(count).toBe(0);
  });
});
