import { mount } from 'svelte';
import './design/tokens.css';
import './design/components.css';
import './app.css';
import App from './App.svelte';
import { loadVersion } from './lib/version.svelte';

mount(App, { target: document.getElementById('app')! });
void loadVersion(); // the beta label also belongs on the sign-in screen, so it does not wait for a session
