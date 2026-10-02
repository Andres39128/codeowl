/** Entrada del dashboard: provider de TanStack Query + App. */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "preact";
import { App } from "./app";
import "./index.css";

const queryClient = new QueryClient({
	defaultOptions: {
		queries: { retry: 1 },
	},
});

render(
	<QueryClientProvider client={queryClient}>
		<App />
	</QueryClientProvider>,
	document.getElementById("app") as HTMLElement,
);
