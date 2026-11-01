import { mount } from 'svelte';
import './design/tokens.css';
import './design/components.css';
import './app.css';
import App from './App.svelte';
import { loadVersion } from './lib/version.svelte';
import { watchBuild } from './lib/build.svelte';

mount(App, { target: document.getElementById('app')! });
void loadVersion(); // the beta label also belongs on the sign-in screen, so it does not wait for a session
watchBuild(); // a page that stays open across an update says so (lib/build.svelte.ts)
