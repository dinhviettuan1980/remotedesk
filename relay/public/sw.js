// Minimal service worker: makes RemoteDesk installable as an app. Everything
// is live (device list, video), so requests always go to the network.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
