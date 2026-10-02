/** Helper de test: QueryClient fresco por test, sin reintentos. */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ComponentChild } from "preact";

export function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: {
			queries: { retry: false },
			mutations: { retry: false },
		},
	});
	return ({ children }: { children: ComponentChild }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}
