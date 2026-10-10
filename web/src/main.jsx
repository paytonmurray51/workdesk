import { render } from 'preact';
import { App } from './app.jsx';
import { applyTheme, loadThemePref, resolveTheme } from './theme.js';
import './styles.css';

// Set the theme before the first paint so day mode doesn't flash dark.
applyTheme(resolveTheme(loadThemePref()));
render(<App />, document.getElementById('app'));
