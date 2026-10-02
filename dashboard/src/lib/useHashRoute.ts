/** Hook de routing hash: estado reactivo sobre el evento hashchange. El hash
 * es la fuente de verdad; App normaliza los desconocidos con resolveRoute. */

import { useEffect, useState } from "preact/hooks";

export function useHashRoute(): string {
	const [hash, setHash] = useState(() => location.hash);

	useEffect(() => {
		const onHashChange = () => setHash(location.hash);
		window.addEventListener("hashchange", onHashChange);
		return () => window.removeEventListener("hashchange", onHashChange);
	}, []);

	return hash;
}
