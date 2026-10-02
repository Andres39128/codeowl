/** Sesión del dashboard sobre TanStack Query: consulta, login y logout. */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, setCsrfToken } from "./apiClient";

export interface User {
	username: string;
	role: "admin" | "member";
	must_change_password: boolean;
}

/** Cuerpo de POST /api/auth/login y GET /api/auth/session (backend auth.go). */
interface AuthResponse {
	user: User;
	csrf_token: string;
}

const SESSION_KEY = ["session"] as const;

/** Estado de sesión + mutaciones de login/logout para el shell. */
export function useSession() {
	const queryClient = useQueryClient();

	const session = useQuery({
		queryKey: SESSION_KEY,
		queryFn: async (): Promise<User | null> => {
			try {
				const data = await api<AuthResponse>("/api/auth/session");
				setCsrfToken(data.csrf_token);
				return data.user;
			} catch (error) {
				// Sin sesión no hay error que mostrar: es el estado previo al login.
				if (error instanceof ApiError && error.status === 401) return null;
				throw error;
			}
		},
		retry: false,
		staleTime: Infinity,
		refetchOnWindowFocus: false,
	});

	const login = useMutation({
		mutationFn: (credentials: { username: string; password: string }) =>
			api<AuthResponse>("/api/auth/login", {
				method: "POST",
				body: credentials,
			}),
		onSuccess: (data) => {
			setCsrfToken(data.csrf_token);
			queryClient.setQueryData(SESSION_KEY, data.user);
		},
	});

	const logout = useMutation({
		mutationFn: () =>
			api<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),
		onSuccess: () => {
			setCsrfToken(null);
			queryClient.setQueryData(SESSION_KEY, null);
		},
	});

	return {
		user: session.data ?? null,
		isLoading: session.isPending,
		error: session.error,
		login,
		logout,
	};
}
