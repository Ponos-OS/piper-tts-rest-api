export function wordCounter(text: string) {
  if (text.trim() === "") {
    return 0;
  }

  return text.trim().split(/\s+/).length;
}
