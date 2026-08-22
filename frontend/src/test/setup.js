import '@testing-library/jest-dom/vitest'

// jsdom implements localStorage, but Vitest's jsdom environment does not surface
// it on the global — sessionStorage arrives and localStorage does not. Rather
// than reshape the app around a gap in the test environment, install the
// missing half here.
//
// Kept to the four methods the app actually uses. If something starts needing
// `length` or `key()`, that is the moment to reach for a fuller shim.
if (typeof window !== 'undefined' && !window.localStorage) {
  const store = new Map()
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key) => (store.has(String(key)) ? store.get(String(key)) : null),
      setItem: (key, value) => store.set(String(key), String(value)),
      removeItem: (key) => store.delete(String(key)),
      clear: () => store.clear(),
    },
  })
}
