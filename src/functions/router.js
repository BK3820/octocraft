const { app } = require('@azure/functions');
const { createApp } = require('../app');
const { createStore } = require('../store');
const { createSecrets } = require('../secrets');

const handle = createApp({ store: createStore(), secrets: createSecrets() });

// Single catch-all HTTP function (host.json sets routePrefix to "") serving the UI and API.
app.http('router', {
  route: '{*path}',
  methods: ['GET', 'POST'],
  authLevel: 'anonymous',
  handler: async (request) => {
    const res = await handle({
      method: request.method,
      url: request.url,
      headers: Object.fromEntries(request.headers),
      body: request.method === 'POST' ? await request.text() : '',
    });
    return { status: res.status, headers: res.headers, body: res.body };
  },
});
