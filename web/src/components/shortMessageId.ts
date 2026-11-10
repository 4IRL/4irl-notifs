/** `01JA9X…7Q`: a message id short enough for a table sub-line. */
export function shortMessageId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 6)}…${id.slice(-2)}` : id;
}
