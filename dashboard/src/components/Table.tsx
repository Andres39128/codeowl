/** Tabla base (guía §5.3): columnas tipadas con render opcional para la cola
 * de triage y el resto de los listados del dashboard. */

import type { ComponentChildren } from "preact";

export interface Column<Row> {
	key: string;
	header: string;
	render?: (row: Row) => ComponentChildren;
}

interface TableProps<Row> {
	columns: Column<Row>[];
	rows: Row[];
	getRowKey?: (row: Row) => string;
	caption?: string;
}

export function Table<Row>({
	columns,
	rows,
	getRowKey,
	caption,
}: TableProps<Row>) {
	return (
		<div class="overflow-x-auto rounded-lg border border-border-subtle bg-bg-surface">
			<table class="w-full text-sm">
				{caption && <caption class="sr-only">{caption}</caption>}
				<thead>
					<tr class="border-b border-border-subtle bg-bg-elevated text-left">
						{columns.map((column) => (
							<th key={column.key} scope="col" class="px-3 py-2 font-medium">
								{column.header}
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{rows.length === 0 && (
						<tr>
							<td
								colSpan={columns.length}
								class="px-3 py-4 text-center text-text-muted"
							>
								Sin datos
							</td>
						</tr>
					)}
					{rows.map((row, index) => (
						<tr
							key={getRowKey ? getRowKey(row) : String(index)}
							class="border-b border-border-subtle last:border-b-0"
						>
							{columns.map((column) => (
								<td key={column.key} class="px-3 py-2">
									{column.render
										? column.render(row)
										: (row as Record<string, ComponentChildren>)[column.key]}
								</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
