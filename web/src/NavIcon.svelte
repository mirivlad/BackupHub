<script lang="ts">
 let {name,size=19}:{name:string;size?:number}=$props();
</script>
<svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
 {#if name==='overview'}<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>
 {:else if name==='jobs'}<rect x="4" y="3" width="16" height="18" rx="2"/><path d="M8 8h8M8 12h8M8 16h5"/><path d="M2 7h4M2 17h4"/>
 {:else if name==='sources'}<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01M11 7.5h7M11 16.5h7"/>
 {:else if name==='storages'}<rect x="3" y="6" width="18" height="12" rx="2"/><path d="M3 11h18M7 15h.01M11 15h.01"/>
 {:else if name==='schedules'}<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M8 3v4M16 3v4M3 10h18"/><path d="M12 14v3l2 1"/>
 {:else if name==='history'}<path d="M3 12a9 9 0 1 0 3-6.7M3 3v5h5"/><path d="M12 7v5l3 2"/>
 {:else if name==='runs'}<path d="M5 4h14M5 9h14M5 14h9M5 19h14"/>
 {:else if name==='audit'}<path d="M12 2 4 6v6c0 5 3.4 8.5 8 10 4.6-1.5 8-5 8-10V6z"/><path d="m9 12 2 2 4-4"/>
 {:else if name==='settings'}<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 1 .4 1.9l-1.6 2.8a1.7 1.7 0 0 1-1.8.7l-1.6-.5a8 8 0 0 1-1.2.7l-.3 1.7H10.7l-.3-1.7a8 8 0 0 1-1.2-.7l-1.6.5a1.7 1.7 0 0 1-1.8-.7l-1.6-2.8a1.7 1.7 0 0 1 .4-1.9l1.2-1a8 8 0 0 1 0-1.4l-1.2-1a1.7 1.7 0 0 1-.4-1.9L5.8 7a1.7 1.7 0 0 1 1.8-.7l1.6.5a8 8 0 0 1 1.2-.7l.3-1.7h2.6l.3 1.7a8 8 0 0 1 1.2.7l1.6-.5a1.7 1.7 0 0 1 1.8.7l1.6 2.8a1.7 1.7 0 0 1-.4 1.9l-1.2 1a8 8 0 0 1 0 1.4z" transform="translate(0,-1) scale(.96)" />
 {:else if name==='folder'}<path d="M3 6.5A2.5 2.5 0 0 1 5.5 4h4l2.5 3H19a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M3 10h18"/>
 {:else if name==='file'}<path d="M6 2h8l5 5v13a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z"/><path d="M14 2v6h5M8 13h7M8 17h7"/>
 {:else if name==='file-other'}<rect x="5" y="4" width="14" height="16" rx="2"/><path d="m9 9 6 6M15 9l-6 6"/>
 {:else if name==='logout'}<path d="M10 17l5-5-5-5M15 12H3"/><path d="M12 3h6a3 3 0 0 1 3 3v12a3 3 0 0 1-3 3h-6"/>
 {:else}<circle cx="12" cy="12" r="9"/>{/if}
</svg>
