// UUID v4 generator: playerId in the API contract is a UUID, so load tests
// mint identifiers in the exact shape (random hex with version 4 bits).
export function uuid4(prefix) {
  const hex = () => Math.floor((1 + Math.random()) * 0x10000).toString(16).slice(1);
  const id = `${hex()}${hex()}-${hex()}-4${hex().slice(0, 3)}-8${hex().slice(0, 3)}-${hex()}${hex()}${hex()}`;
  return id;
}
