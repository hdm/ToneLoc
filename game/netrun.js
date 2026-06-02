/* ToneLoc/Go :: NETRUNNER -- a network-infiltration roguelike.
 *
 * You start at HOME. Scan to find nodes; break into them with a turn-based
 * exploit "fight" (pick tools, guess passwords); use each foothold to pivot
 * deeper for loot and intel. But the moment you're live on the wire a TRACE
 * starts crawling back along your connection toward HOME -- if it reaches your
 * home node while you're connected, you're BUSTED. Grab the objective and go
 * dark to win.
 *
 * The world is built from window.TONELOC_SEED (game/seed.js), generated from
 * zmap-go's iterator or a real zmap scan. Pure browser, no backend, no network.
 */
(function () {
  "use strict";

  // ---- DOS palette + cell-buffer canvas renderer --------------------------
  const PAL = ["#000000","#0000AA","#00AA00","#00AAAA","#AA0000","#AA00AA","#AA5500","#AAAAAA",
    "#555555","#5555FF","#55FF55","#55FFFF","#FF5555","#FF55FF","#FFFF55","#FFFFFF"];
  const C = { BLACK:0,BLUE:1,GREEN:2,CYAN:3,RED:4,MAGENTA:5,BROWN:6,LGRAY:7,
    DGRAY:8,LBLUE:9,LGREEN:10,LCYAN:11,LRED:12,LMAG:13,YELLOW:14,WHITE:15 };
  const W=80, H=25, CW=11, CH=22, FS=16;

  function Screen(cv){ this.cv=cv; cv.width=W*CW; cv.height=H*CH; this.ctx=cv.getContext("2d");
    this.cells=new Array(W*H); this.clear(C.LGRAY,C.BLACK); }
  Screen.prototype.clear=function(fg,bg){ for(let i=0;i<W*H;i++) this.cells[i]={ch:" ",fg,bg}; };
  Screen.prototype.set=function(x,y,ch,fg,bg){ if(x<0||y<0||x>=W||y>=H)return; this.cells[y*W+x]={ch:ch||" ",fg,bg}; };
  Screen.prototype.print=function(x,y,fg,bg,s){ s=String(s); for(let i=0;i<s.length&&x<W;i++,x++) this.set(x,y,s[i],fg,bg); };
  Screen.prototype.fill=function(x,y,w,h,ch,fg,bg){ for(let yy=y;yy<y+h;yy++)for(let xx=x;xx<x+w;xx++)this.set(xx,yy,ch,fg,bg); };
  const BOX={d:["╔","╗","╚","╝","═","║"],s:["┌","┐","└","┘","─","│"]};
  Screen.prototype.box=function(x,y,w,h,fg,bg,dbl){ const b=dbl?BOX.d:BOX.s;
    this.set(x,y,b[0],fg,bg);this.set(x+w-1,y,b[1],fg,bg);this.set(x,y+h-1,b[2],fg,bg);this.set(x+w-1,y+h-1,b[3],fg,bg);
    for(let i=1;i<w-1;i++){this.set(x+i,y,b[4],fg,bg);this.set(x+i,y+h-1,b[4],fg,bg);}
    for(let i=1;i<h-1;i++){this.set(x,y+i,b[5],fg,bg);this.set(x+w-1,y+i,b[5],fg,bg);} };
  Screen.prototype.title=function(x,y,w,fg,bg,t){ t="► "+t+" ◄"; this.print(x+((w-t.length)>>1),y,fg,bg,t); };
  Screen.prototype.meter=function(x,y,w,frac,fg,track,bg){ frac=Math.max(0,Math.min(1,frac)); const f=(frac*w)|0;
    for(let i=0;i<w;i++) this.set(x+i,y, i<f?"█":"▒", i<f?fg:track, bg); };
  Screen.prototype.render=function(){ const ctx=this.ctx; ctx.textBaseline="top";
    ctx.font=FS+'px "DejaVu Sans Mono","Cascadia Mono","Consolas","Menlo",monospace';
    for(let y=0;y<H;y++)for(let x=0;x<W;x++){ const c=this.cells[y*W+x],px=x*CW,py=y*CH;
      ctx.fillStyle=PAL[c.bg]; ctx.fillRect(px,py,CW,CH);
      if(c.ch!==" "&&c.ch!==""){ ctx.fillStyle=PAL[c.fg]; ctx.fillText(c.ch,px+1,py+3); } } };
  const pad=(s,n)=>{ s=String(s); return s.length>=n?s.slice(0,n):s+" ".repeat(n-s.length); };
  const rpad=(s,n)=>{ s=String(s); return s.length>=n?s.slice(s.length-n):" ".repeat(n-s.length)+s; };
  const center=(scr,y,fg,bg,s)=>scr.print((W-s.length)>>1,y,fg,bg,s);

  // ---- rng/hash ------------------------------------------------------------
  function hash32(s){ let h=0x811c9dc5>>>0; for(let i=0;i<s.length;i++){h^=s.charCodeAt(i);h=Math.imul(h,0x01000193)>>>0;} return h>>>0; }
  function rng(seed){ let a=seed>>>0; return ()=>{ a|=0;a=a+0x6D2B79F5|0; let t=Math.imul(a^a>>>15,1|a); t=t+Math.imul(t^t>>>7,61|t)^t; return ((t^t>>>14)>>>0)/4294967296; }; }
  const pick=(r,arr)=>arr[(r()*arr.length)|0];

  // ---- seed world ----------------------------------------------------------
  const SEED = window.TONELOC_SEED || { mask:"10.0.0.0/24", hosts:{}, honeypots:[] };
  const HONEY = new Set((SEED.honeypots||[]).map(h=>h.split(":")[0]));

  const PW_POOL = ["password","123456","admin","root","letmein","qwerty","dragon","ncc1701",
    "trustno1","hunter2","changeme","monkey","master","joshua","wargames","pencil","setec","z1on0101"];
  const KINDS = ["workstation","fileserver","webserver","mailserver","database","dc"];
  const KIND_GLYPH = { home:"⌂", uplink:"↑", router:"#", workstation:"■", fileserver:"▦",
    webserver:"◰", mailserver:"✉", database:"▤", dc:"★" };

  // Build the network graph from the seed data.
  function buildWorld(seedNum){
    const r = rng(seedNum);
    const byIp = {};
    for (const key in SEED.hosts){ const [ip,port]=key.split(":"); (byIp[ip]||(byIp[ip]=[])).push(Object.assign({port:+port}, SEED.hosts[key])); }
    const bySub = {};
    for (const ip in byIp){ const net=ip.split(".").slice(0,3).join("."); (bySub[net]||(bySub[net]=[])).push(ip); }
    let subs = Object.keys(bySub).map(net=>({net, ips:bySub[net]})).sort((a,b)=>b.ips.length-a.ips.length);
    subs = subs.slice(0, 6);

    const nodes=[]; let id=0;
    const mk=(o)=>{ o.id=id++; o.links=[]; o.state=o.state||"hidden"; o.alert=0; nodes.push(o); return o; };
    const link=(a,b)=>{ if(!a.links.includes(b.id))a.links.push(b.id); if(!b.links.includes(a.id))b.links.push(a.id); };

    const home = mk({kind:"home", host:"HOME", ip:"127.0.0.1", ports:[], state:"owned", loot:0, shield:0});
    const uplink = mk({kind:"uplink", host:"uplink-gw", ip:"0.0.0.0", ports:[], state:"owned", loot:0, shield:0});
    link(home, uplink);

    const routers=[];
    subs.forEach((s,si)=>{
      const rr = mk({kind:"router", host:"gw."+s.net, ip:s.net+".1", subnet:s.net,
        ports:[{port:443,svc:"https"},{port:23,svc:"telnet"}], shield:2+(si>2?1:0), alertMax:4,
        loot:40+((r()*40)|0), honeypot:false, depth:1});
      routers.push(rr);
      // first two subnets reachable directly from uplink; deeper ones need a pivot.
      s.ips.slice(0, 5).forEach((ip,hi)=>{
        const ports = byIp[ip].slice(0,4).map(h=>({port:h.port, svc:h.svc||("tcp/"+h.port), banner:h.banner||""}));
        const kr = rng(hash32(ip));
        const kind = pick(kr, KINDS);
        const isObj = false;
        const hp = HONEY.has(ip);
        const node = mk({kind, host:hostname(kind,ip,kr), ip, subnet:s.net, ports,
          shield: 2 + ((kr()*3)|0), alertMax: 3 + ((kr()*2)|0),
          loot: 20 + ((kr()*100)|0), honeypot: hp, depth:2,
          secret: PW_POOL[(kr()*PW_POOL.length)|0], hasLogin: ports.some(p=>isLogin(p.svc)),
          tool: kr()<0.3 ? pick(kr, ["bof","zero"]) : null,
          intelFor: null });
        link(rr, node);
      });
    });
    // backbone: uplink -> first 2 routers directly; remaining routers hang off a
    // random already-reachable host (a pivot you must own to even see them).
    routers.forEach((rr,i)=>{
      if (i<2){ link(uplink, rr); }
      else {
        const reach = routers.slice(0,i).flatMap(x=>x.links).map(idx=>nodes[idx]).filter(n=>n && n.kind!=="router" && n.kind!=="uplink");
        const pivot = reach.length ? reach[(r()*reach.length)|0] : uplink;
        link(pivot, rr); rr.depth = (pivot.depth||1)+1;
        nodes.filter(n=>n.subnet===rr.subnet && n!==rr).forEach(n=>n.depth=rr.depth+1);
      }
    });

    // Objective: the deepest, juiciest host.
    const cands = nodes.filter(n=>n.kind!=="home"&&n.kind!=="uplink"&&n.kind!=="router");
    cands.sort((a,b)=>(b.depth-a.depth)||(b.loot-a.loot));
    const obj = cands[0];
    if (obj){ obj.objective=true; obj.kind="dc"; obj.host="MAINFRAME"; obj.loot=600; obj.shield=5; obj.alertMax=5; }

    // Some hosts carry intel: the password for a linked locked node.
    nodes.forEach(n=>{ if(n.loot>0){ const lr=rng(hash32(n.ip+"#i"));
      if (lr()<0.4){ const nb=n.links.map(i=>nodes[i]).filter(x=>x.secret && x!==n);
        if (nb.length) n.intelFor = nb[(lr()*nb.length)|0].id; } } });

    return { nodes, home, uplink, obj };
  }
  function isLogin(svc){ return ["telnet","ssh","ftp","pop3","telnets"].includes(svc); }
  function hostname(kind,ip,r){ const a=["corp","acme","dev","ops","hr","fin","lab","vpn","mail","web","db","ad"];
    const tag=a[(r()*a.length)|0]; const last=ip.split(".")[3]; return tag+"-"+kind.slice(0,3)+last; }

  // ---- tools ---------------------------------------------------------------
  const TOOLS = {
    dict:  {name:"brutus (dict)",  vs:["telnet","ssh","ftp","pop3","telnets"], dmg:1, alert:1, heat:3, odds:.65},
    web:   {name:"Web Exploit",   vs:["http","https","http-alt"],             dmg:2, alert:1, heat:4, odds:.7},
    creds: {name:"Default Creds", vs:["telnet","ssh","ftp","http-alt","https"],dmg:2, alert:2, heat:2, odds:.55},
    bof:   {name:"Buffer Ovflw",  vs:["*"],                                    dmg:3, alert:2, heat:6, odds:.5,  limited:true},
    phish: {name:"Phishing",      vs:["*"],                                    dmg:1, alert:0, heat:1, odds:.45},
    zero:  {name:"0-DAY",         vs:["*"],                                    dmg:9, alert:0, heat:8, odds:1,   limited:true},
  };

  // ---- game state ----------------------------------------------------------
  const SCR = { TITLE:0, MAP:1, INF:2, WIN:3, LOSE:4, HELP:5 };
  let scr, G;
  function newGame(seedNum){
    const world = buildWorld(seedNum);
    // reveal uplink's direct links so there's somewhere to start.
    world.uplink.links.forEach(i=>{ if(world.nodes[i].state==="hidden") world.nodes[i].state="discovered"; });
    G = {
      screen: SCR.TITLE, world, seedNum,
      sel: 0, scroll: 0,
      credits: 0, objectiveTaken: false,
      connected: true, trace: 0, heatSpike: 0,
      inv: { dict:99, web:99, creds:99, phish:99, bof:1, zero:0 },
      knownPw: {}, // nodeId -> true
      msg: "", msgUntil: 0,
      inf: null,
      log: ["NETRUNNER online. Welcome home, operator."],
      frame: 0, lastTs: 0,
      high: +(localStorage.getItem("netrun_high")||0),
      result: null,
    };
  }
  function log(s){ G.log.push(s); if(G.log.length>120)G.log.shift(); }
  function setMsg(s,ms){ G.msg=s; G.msgUntil=performance.now()+(ms||1800); }
  const N = (i)=>G.world.nodes[i];

  // Visible (discovered/owned) nodes, in a tidy tree order for the list.
  function visible(){
    const out=[]; const W2=G.world;
    const add=(n,d)=>out.push({n, depth:d});
    add(W2.home,0); add(W2.uplink,1);
    W2.nodes.filter(n=>n.kind==="router"&&n.state!=="hidden").forEach(rr=>{
      add(rr,2);
      W2.nodes.filter(n=>n.subnet===rr.subnet && n!==rr && n.state!=="hidden").forEach(h=>add(h,3));
    });
    return out;
  }

  // ---- actions -------------------------------------------------------------
  function curList(){ return visible(); }
  function selNode(){ const l=curList(); return l[Math.min(G.sel,l.length-1)]?.n; }

  function depthOf(n){ return n.depth||0; }
  function ownedDepth(){ let d=0; G.world.nodes.forEach(n=>{ if(n.state==="owned"&&n.kind!=="home"&&n.kind!=="uplink") d=Math.max(d,depthOf(n)); }); return d; }

  function scan(n){
    if(!G.connected){ setMsg("You are DARK. Reconnect (R) to scan.",1600); return; }
    if(n.state!=="owned"){ setMsg("Can only scan from a node you OWN.",1500); return; }
    let found=0; n.links.forEach(i=>{ const m=N(i); if(m.state==="hidden"){ m.state="discovered"; found++; } });
    G.trace=Math.min(100,G.trace+2);
    log("$ scan "+(n.ip)+"  ::  "+found+" new node(s) found");
    setMsg(found?("SCAN: "+found+" node(s) revealed"):"scan: nothing new", 1400);
    Sound.dial();
  }

  function loot(n){
    if(n.state!=="owned"){ setMsg("Compromise it first.",1400); return; }
    if(n.looted){ setMsg("Already looted.",1200); return; }
    n.looted=true; G.credits+=n.loot;
    log("$ exfil "+n.ip+"  ::  +"+n.loot+" cr");
    let extra="";
    if(n.tool && G.inv[n.tool]!==undefined){ G.inv[n.tool]++; extra=" + "+TOOLS[n.tool].name; }
    if(n.intelFor!=null && !G.knownPw[n.intelFor]){ G.knownPw[n.intelFor]=true; extra+=" + creds intel"; }
    if(n.objective){ G.objectiveTaken=true; log("*** OBJECTIVE DATA EXFILTRATED -- now go DARK to win! ***"); setMsg("OBJECTIVE TAKEN! Disconnect (R) at HOME to WIN",3000); Sound.levelup(); }
    else setMsg("Looted +"+n.loot+" cr"+extra, 1800);
    Sound.connect();
  }

  function toggleConnect(){
    G.connected=!G.connected;
    if(G.connected){ log("link UP -- you are live on the wire."); setMsg("CONNECTED -- trace active",1400); }
    else { log("link DOWN -- going dark. Trace cooling."); setMsg("DARK -- safe, but blind to the net",1600);
      // Win check: objective taken and you went dark.
      if(G.objectiveTaken){ win(); } }
  }

  function startInfiltrate(n){
    if(!G.connected){ setMsg("You are DARK. Reconnect (R) first.",1500); return; }
    if(n.state==="owned"){ setMsg("Already pwned. Loot it (L) or scan (S).",1500); return; }
    if(n.state!=="discovered"){ setMsg("Scan to reveal it first.",1400); return; }
    if(n.kind==="uplink"||n.kind==="home"){ return; }
    n.shieldCur = (n.shieldCur==null)? n.shield : n.shieldCur;
    n.alert = 0;
    G.inf = { n, tool:firstTool(n), vec:0, mode:"tools", pwSel:0, log:[
      "Connected to "+n.ip+" ("+n.host+")",
      "nerva: "+(n.ports.map(p=>p.svc+"/"+p.port).join("  ")||"no services") ] };
    G.screen=SCR.INF;
    log("$ nerva "+n.ip+"  ::  brutus armed ...");
  }
  function firstTool(n){ const ids=toolIds(); for(const id of ids){ if(applies(TOOLS[id], curVecSvc(n,0))) return ids.indexOf(id); } return 0; }
  function toolIds(){ return Object.keys(TOOLS).filter(id=>!TOOLS[id].limited || G.inv[id]>0); }
  function curVecSvc(n,vi){ return n.ports.length? n.ports[Math.min(vi,n.ports.length-1)].svc : "*"; }
  function applies(tool, svc){ return tool.vs.includes("*") || tool.vs.includes(svc); }

  function infiltrateAttempt(){
    const inf=G.inf, n=inf.n; const ids=toolIds(); const id=ids[Math.min(inf.tool,ids.length-1)];
    const tool=TOOLS[id]; const svc=curVecSvc(n, inf.vec);
    if(TOOLS[id].limited && G.inv[id]<=0){ inf.log.push("! out of "+tool.name); return; }
    const ok = applies(tool, svc);
    let r = rng(hash32(n.ip+id+svc+n.alert+inf.log.length))();
    G.trace=Math.min(100, G.trace + tool.heat*0.25);
    if(TOOLS[id].limited) G.inv[id]--;
    if(n.honeypot){ n.alert += 2; G.trace=Math.min(100,G.trace+22); G.heatSpike=12;
      inf.log.push("!! IDS TRIPPED -- this is a HONEYPOT! trace +22"); Sound.busy();
      if(n.alert>=n.alertMax){ kicked(); } return; }
    if(ok && r < tool.odds){ n.shieldCur -= tool.dmg; inf.log.push("» "+tool.name+" vs "+svc+": BREACH (-"+tool.dmg+" shield)"); Sound.dial(); }
    else { n.alert += tool.alert||1; inf.log.push("» "+tool.name+" vs "+svc+(ok?": failed":": no effect on "+svc)); }
    n.alert += 0; // (tool.alert already applied on fail; success is quiet)
    if(n.shieldCur<=0){ owned(n); return; }
    if(n.alert>=n.alertMax){ kicked(); }
  }
  function tryPassword(idx){
    const inf=G.inf, n=inf.n; const list=pwList(n); const guess=list[idx];
    G.trace=Math.min(100,G.trace+1);
    if(guess===n.secret){ inf.log.push("» login "+guess+" : ACCESS GRANTED"); n.shieldCur=0; owned(n); }
    else { n.alert++; inf.log.push("» login "+guess+" : denied"); Sound.busy(); if(n.alert>=n.alertMax) kicked(); }
  }
  function pwList(n){
    const r=rng(hash32(n.ip+"#pw"));
    const set=new Set([n.secret]); while(set.size<5) set.add(PW_POOL[(r()*PW_POOL.length)|0]);
    const arr=[...set]; for(let i=arr.length-1;i>0;i--){ const j=(r()*(i+1))|0; const t=arr[i];arr[i]=arr[j];arr[j]=t; }
    return arr;
  }
  function owned(n){ n.state="owned"; G.trace=Math.min(100,G.trace+3);
    log("+ ROOT on "+n.ip+" ("+n.host+")"); setMsg("ACCESS GRANTED :: "+n.host,2000); Sound.connect();
    // owning a node auto-reveals its direct links as discovered (you can see the wire).
    n.links.forEach(i=>{ if(N(i).state==="hidden") N(i).state="discovered"; });
    G.inf=null; G.screen=SCR.MAP; }
  function kicked(){ const n=G.inf.n; n.shieldCur=n.shield; G.trace=Math.min(100,G.trace+8);
    log("- connection to "+n.ip+" dropped (alarm)"); setMsg("KICKED OUT -- alarm raised",1800); Sound.busy();
    G.inf=null; G.screen=SCR.MAP; }

  function win(){ G.screen=SCR.WIN; G.connected=false; const bonus=Math.round((100-G.trace)*10);
    G.score=G.credits+bonus; if(G.score>G.high){G.high=G.score;localStorage.setItem("netrun_high",G.high);} Sound.levelup(); }
  function lose(){ G.screen=SCR.LOSE; G.score=G.credits;
    if(G.score>G.high){G.high=G.score;localStorage.setItem("netrun_high",G.high);} Sound.gameover(); }

  // ---- main loop -----------------------------------------------------------
  function tick(ts){
    const dt = G.lastTs? (ts-G.lastTs)/1000 : 0; G.lastTs=ts; G.frame++;
    if(G.screen===SCR.MAP || G.screen===SCR.INF){
      if(G.connected){
        const rate = 0.9 + 0.7*ownedDepth() + (G.objectiveTaken?2.5:0);
        G.trace = Math.min(100, G.trace + rate*dt);
        if(G.heatSpike>0) G.heatSpike-=dt*6;
        if(G.trace>=100){ lose(); }
      } else {
        G.trace = Math.max(0, G.trace - 22*dt);
      }
    }
    draw();
    requestAnimationFrame(tick);
  }

  // ---- drawing -------------------------------------------------------------
  function draw(){
    const blink=((G.frame>>4)&1)===0;
    scr.clear(C.LGRAY,C.BLACK);
    switch(G.screen){
      case SCR.TITLE: drawTitle(blink); break;
      case SCR.MAP: drawMap(blink); break;
      case SCR.INF: drawInf(blink); break;
      case SCR.WIN: drawEnd(blink,true); break;
      case SCR.LOSE: drawEnd(blink,false); break;
      case SCR.HELP: drawHelp(); break;
    }
    scr.render();
  }

  const FONT={T:["█████","  █  ","  █  ","  █  ","  █  "],N:["█   █","██  █","█ █ █","█  ██","█   █"],
    E:["█████","█    ","███  ","█    ","█████"],R:["████ ","█   █","████ ","█  █ ","█   █"],
    U:["█   █","█   █","█   █","█   █"," ███ "]," ":["  ","  ","  ","  ","  "]};
  function banner(w){ const rows=["","","","",""]; for(const ch of w){ const g=FONT[ch]||FONT[" "]; for(let r=0;r<5;r++)rows[r]+=g[r]+" "; } return rows; }

  function drawTitle(blink){
    for(let y=0;y<H;y++)for(let x=0;x<W;x++)scr.set(x,y,y%2?"░":" ",C.DGRAY,C.BLACK);
    const logo=banner("NETRUNNER"); const lx=(W-logo[0].length)>>1;
    const pal=[C.LGREEN,C.LCYAN,C.CYAN,C.LBLUE,C.GREEN];
    for(let r=0;r<5;r++) scr.print(lx,2+r,pal[(((G.frame/3)|0)+r)%pal.length],C.BLACK,logo[r]);
    center(scr,8,C.YELLOW,C.BLACK,"a ToneLoc/Go infiltration roguelike");
    center(scr,10,C.WHITE,C.BLACK,"SCAN with nerva  ::  BREACH with brutus tools & password sprays");
    center(scr,11,C.WHITE,C.BLACK,"PIVOT deeper for LOOT  ::  grab the MAINFRAME and go DARK");
    center(scr,13,C.LRED,C.BLACK,"but a TRACE crawls home while you're connected --");
    center(scr,14,C.LRED,C.BLACK,"let it reach HOME and you're BUSTED. disconnect to cool it.");
    const m=SEED.meta||{};
    center(scr,16,C.DGRAY,C.BLACK,"world: "+(SEED.mask||"?")+"   seed: "+(m.source||"synthetic"));
    if(blink) center(scr,19,C.LGREEN,C.BLACK,"> > >   PRESS  ENTER  TO  JACK IN   < < <");
    center(scr,21,C.YELLOW,C.BLACK,"BEST HAUL  "+rpad(G.high,7)+" cr");
    scr.print(0,24,C.BLACK,C.LGRAY,pad(" ENTER:start   ?:help   arrows/jk:move   ESC:title",W));
  }

  function nodeIcon(n){
    if(n.state==="owned") return ["●",C.LGREEN];
    if(n.state==="discovered") return ["◌",C.LCYAN];
    return ["?",C.DGRAY];
  }
  function drawMap(blink){
    // Header.
    scr.print(0,0,C.BLACK,G.connected?C.GREEN:C.RED,pad(
      " NETRUNNER  "+(G.connected?"● LIVE":"○ DARK")+"   credits:"+G.credits+"   tools:"+toolIds().length+"   ESC:title",W));
    // Network tree (left).
    scr.box(0,1,50,21,C.LCYAN,C.BLUE,true); scr.title(0,1,50,C.YELLOW,C.BLUE,"Network");
    const list=curList(); const rows=19;
    if(G.sel>=list.length)G.sel=list.length-1; if(G.sel<0)G.sel=0;
    if(G.sel<G.scroll)G.scroll=G.sel; if(G.sel>=G.scroll+rows)G.scroll=G.sel-rows+1;
    for(let i=0;i<rows;i++){ const e=list[i+G.scroll]; if(!e)break; const n=e.n, y=2+i;
      const sel=(i+G.scroll===G.sel); const bg=sel?C.CYAN:C.BLUE; const [ic,icc]=nodeIcon(n);
      const g=KIND_GLYPH[n.kind]||"■";
      let line=" ".repeat(e.depth)+g+" "+(n.host||n.ip);
      scr.print(1,y,sel?C.BLACK:C.LGRAY,bg,pad(line,40));
      scr.set(2+0,y,g,sel?C.BLACK:C.WHITE,bg);
      scr.set(45,y,ic,sel?C.BLACK:icc,bg);
      if(n.objective) scr.set(47,y,"⚑",sel?C.BLACK:C.LRED,bg);
      else if(n.looted) scr.set(47,y,"·",sel?C.BLACK:C.DGRAY,bg);
      else if(n.loot>0 && n.state==="owned") scr.set(47,y,"$",sel?C.BLACK:C.YELLOW,bg);
      if(G.knownPw[n.id]) scr.set(48,y,"⚷",sel?C.BLACK:C.LMAG,bg);
    }
    // Detail panel (right).
    const n=selNode(); scr.box(50,1,30,15,C.LGRAY,C.BLACK,true); scr.title(50,1,30,C.WHITE,C.BLACK,"Node");
    if(n){
      let y=3; const I=(l,v,c)=>{ scr.print(52,y,C.YELLOW,C.BLACK,l); scr.print(52+l.length,y,c||C.WHITE,C.BLACK,String(v)); y++; };
      I("host : ", n.host);
      I("ip   : ", n.ip);
      I("type : ", n.kind+(n.objective?"  ⚑OBJECTIVE":""));
      I("state: ", n.state, n.state==="owned"?C.LGREEN:n.state==="discovered"?C.LCYAN:C.DGRAY);
      if(n.kind!=="home"&&n.kind!=="uplink"){
        I("shield:", (n.state==="owned"?"--":(n.shieldCur??n.shield))+"/"+n.shield);
        I("loot : ", n.loot+" cr"+(n.looted?" (taken)":""));
      }
      y++;
      scr.print(52,y++,C.LCYAN,C.BLACK,"services:");
      (n.ports||[]).slice(0,4).forEach(p=>{ scr.print(53,y++,isLogin(p.svc)?C.LMAG:C.LGRAY,C.BLACK,pad(p.svc+"/"+p.port,24)); });
      if(!(n.ports||[]).length) scr.print(53,y++,C.DGRAY,C.BLACK,"(none)");
    }
    // Status + trace (right bottom).
    scr.box(50,16,30,6,C.LRED,C.BLACK,true); scr.title(50,16,30,C.WHITE,C.BLACK,"Status");
    scr.print(52,17,C.YELLOW,C.BLACK,"link : "); scr.print(59,17,G.connected?C.LGREEN:C.LRED,C.BLACK,G.connected?"LIVE":"DARK");
    scr.print(52,18,C.YELLOW,C.BLACK,"depth: "); scr.print(59,18,C.WHITE,C.BLACK,String(ownedDepth()));
    const tc=G.trace>80?C.LRED:G.trace>55?C.YELLOW:C.LGREEN;
    scr.print(52,19,C.LRED,C.BLACK,"TRACE→HOME");
    scr.meter(52,20,26,G.trace/100,tc,C.DGRAY,C.BLACK);
    scr.print(52,21,tc,C.BLACK,rpad(Math.floor(G.trace)+"%",4)+(G.objectiveTaken?"  *OBJ! GO DARK*":""));
    // Activity log strip + command bar.
    const last=G.log.slice(-1)[0]||""; scr.print(0,22,C.LGREEN,C.BLACK,pad("» "+last,W));
    const n2=selNode(); const act = !n2?"":
      n2.state==="owned" ? "ENTER/S:scan  L:loot" :
      n2.state==="discovered" ? "ENTER/I:infiltrate" : "?";
    scr.print(0,23,C.BLACK,C.LGRAY,pad(" "+act+"   R:"+(G.connected?"go dark":"connect")+"   arrows:move   ?:help   ESC:title",W));
    let msg=(performance.now()<G.msgUntil)?G.msg:"";
    scr.print(0,24,C.BLACK,C.BLACK,pad("",W));
    if(msg){ const a=(G.heatSpike>0)?(blink?C.LRED:C.YELLOW):C.LCYAN; center(scr,24,a,C.BLACK,msg); }
  }

  function drawInf(blink){
    const inf=G.inf; if(!inf){ G.screen=SCR.MAP; return; } const n=inf.n;
    scr.print(0,0,C.BLACK,C.RED,pad(" INFILTRATE  "+n.host+"  ("+n.ip+")   ESC:abort",W));
    // Target shield/alert.
    scr.box(0,1,40,8,C.LRED,C.BLACK,true); scr.title(0,1,40,C.WHITE,C.BLACK,"Target");
    scr.print(2,3,C.YELLOW,C.BLACK,"SHIELD"); scr.meter(9,3,28,(n.shieldCur??n.shield)/Math.max(1,n.shield),C.LCYAN,C.DGRAY,C.BLACK);
    scr.print(2,4,C.WHITE,C.BLACK,(n.shieldCur??n.shield)+" / "+n.shield);
    scr.print(2,5,C.YELLOW,C.BLACK,"ALARM "); scr.meter(9,5,28,n.alert/Math.max(1,n.alertMax),C.LRED,C.DGRAY,C.BLACK);
    scr.print(2,6,C.WHITE,C.BLACK,n.alert+" / "+n.alertMax+(n.honeypot?"   ☠ feels like a trap...":""));
    // Vectors (services).
    scr.box(40,1,40,8,C.LGRAY,C.BLACK,true); scr.title(40,1,40,C.WHITE,C.BLACK,"Vectors");
    (n.ports.length?n.ports:[{svc:"*",port:0}]).slice(0,5).forEach((p,i)=>{ const selv=i===inf.vec;
      scr.print(42,3+i,selv?C.BLACK:(isLogin(p.svc)?C.LMAG:C.LGRAY),selv?C.CYAN:C.BLACK,pad((selv?"►":" ")+p.svc+"/"+p.port,36)); });
    // Tools.
    scr.box(0,9,40,10,C.LGREEN,C.BLACK,true); scr.title(0,9,40,C.WHITE,C.BLACK,"Tools / Exploits");
    const ids=toolIds(); const svc=curVecSvc(n,inf.vec);
    ids.forEach((id,i)=>{ const t=TOOLS[id]; const selt=i===inf.tool; const ok=applies(t,svc);
      const c=selt?C.BLACK:(ok?C.LGREEN:C.DGRAY); const bg=selt?C.GREEN:C.BLACK;
      const lim=t.limited?(" x"+G.inv[id]):"";
      scr.print(2,11+i,c,bg,pad((selt?"►":" ")+t.name+lim,24));
      scr.print(27,11+i,selt?C.BLACK:C.YELLOW,bg,ok?(Math.round(t.odds*100)+"%"):"n/a"); });
    if(n.hasLogin) scr.print(2,18,C.LMAG,C.BLACK,"[G] brutus password spray");
    // Log.
    scr.box(40,9,40,10,C.LCYAN,C.BLACK,true); scr.title(40,9,40,C.WHITE,C.BLACK,"Session");
    inf.log.slice(-8).forEach((l,i)=>{ let c=C.LGRAY; if(l.indexOf("BREACH")>=0||l.indexOf("GRANTED")>=0)c=C.LGREEN;
      else if(l.indexOf("IDS")>=0||l.indexOf("denied")>=0)c=C.LRED; scr.print(42,11+i,c,C.BLACK,pad(l,36)); });
    // Trace strip + commands.
    const tc=G.trace>80?C.LRED:G.trace>55?C.YELLOW:C.LGREEN;
    scr.print(0,19,C.LRED,C.BLACK,"TRACE "); scr.meter(6,19,40,G.trace/100,tc,C.DGRAY,C.BLACK); scr.print(47,19,tc,C.BLACK,Math.floor(G.trace)+"%");
    if(inf.mode==="pw"){
      scr.fill(10,8,60,11," ",C.WHITE,C.BLUE); scr.box(10,8,60,11,C.LCYAN,C.BLUE,true); scr.title(10,8,60,C.YELLOW,C.BLUE,"Guess Password");
      const list=pwList(n);
      list.forEach((p,i)=>{ const sel=i===inf.pwSel; const known=G.knownPw[n.id]&&p===n.secret;
        scr.print(14,10+i,sel?C.BLACK:(known?C.LGREEN:C.WHITE),sel?C.CYAN:C.BLUE,pad((sel?"► ":"  ")+p+(known?"   ← intel":""),52)); });
      scr.print(12,8+11-2,C.LGRAY,C.BLUE,"ENTER:try   ESC:back   ↑↓:choose");
    }
    scr.print(0,23,C.BLACK,C.LGRAY,pad(" ↑↓:tool  ←→:vector  ENTER:attack  G:password  ESC:abort",W));
    let msg=(performance.now()<G.msgUntil)?G.msg:""; if(msg) center(scr,24,blink?C.YELLOW:C.LRED,C.BLACK,msg);
  }

  function drawEnd(blink,won){
    const bg=won?C.GREEN:C.RED;
    for(let y=0;y<H;y++)for(let x=0;x<W;x++)scr.set(x,y,y%2?"▒":" ",won?C.GREEN:C.RED,C.BLACK);
    center(scr,5,won?C.LGREEN:C.LRED,C.BLACK,"████████████████████████████████████████");
    center(scr,6,blink?C.WHITE:(won?C.LGREEN:C.LRED),C.BLACK, won?"   D A T A   E X F I L T R A T E D   ":"   C O N N E C T I O N   T R A C E D   ");
    center(scr,7,won?C.LGREEN:C.LRED,C.BLACK,"████████████████████████████████████████");
    center(scr,9,C.YELLOW,C.BLACK, won?"You vanished into the noise. Clean job.":"They kicked your door in. BUSTED.");
    center(scr,12,C.WHITE,C.BLACK,"Credits looted   "+G.credits);
    if(won) center(scr,13,C.LCYAN,C.BLACK,"Stealth bonus    "+Math.max(0,Math.round((100-G.trace)*10)));
    center(scr,15,C.LGREEN,C.BLACK,"SCORE   "+(G.score||G.credits));
    center(scr,16,C.YELLOW,C.BLACK,((G.score||G.credits)>=G.high?"** NEW BEST HAUL **  ":"BEST HAUL  ")+G.high);
    if(blink) center(scr,19,C.WHITE,C.BLACK,"PRESS ENTER TO RUN AGAIN");
    scr.print(0,24,C.BLACK,C.LGRAY,pad(" ENTER:new run   ESC:title",W));
  }

  function drawHelp(){
    scr.box(2,1,76,23,C.LCYAN,C.BLACK,true); scr.title(2,1,76,C.YELLOW,C.BLACK,"NETRUNNER -- how to run");
    const L=[
      "",
      " GOAL  Pivot through the network, exfiltrate the MAINFRAME (⚑), then go",
      "       DARK to bank it. Grab credits along the way. Don't get traced.",
      "",
      " THE TRACE  While your link is LIVE a trace crawls toward HOME. The deeper",
      "       you sit and the longer you stay connected, the faster it climbs.",
      "       Hit 100% and the trace reaches HOME -> BUSTED. Press R to go DARK:",
      "       you're safe and the trace cools, but you can't scan or breach while",
      "       dark. Honeypots (☠) spike the trace hard.",
      "",
      " LOOP  S/ENTER  scan from an OWNED node to reveal its neighbours",
      "       I/ENTER  infiltrate a DISCOVERED node (the breach mini-game)",
      "       L        loot an owned node (credits, tools, password intel ⚷)",
      "       R        toggle LIVE / DARK",
      "",
      " BREACH  Pick a vector (service) and a tool/exploit; ENTER to attack and",
      "       drop the target's SHIELD. Each try raises the ALARM -- max it and",
      "       you're kicked. For login services press G to guess the password",
      "       (looted intel ⚷ reveals the right one).",
      "",
      "       ENTER: back to the run",
    ];
    L.forEach((l,i)=> scr.print(4,2+i, l.indexOf("GOAL")>=0||l.indexOf("THE TRACE")>=0||l.indexOf("LOOP")>=0||l.indexOf("BREACH")>=0?C.YELLOW:C.LGRAY, C.BLACK, l));
  }

  // ---- input ---------------------------------------------------------------
  function onKey(e){
    Sound.resume(); const k=e.key, kl=k.toLowerCase();
    if(G.screen===SCR.TITLE){ if(k==="Enter"){ const sn=G.seedNum; newGame(sn); G.screen=SCR.MAP; } else if(kl==="?"||kl==="h"){G.screen=SCR.HELP;} return; }
    if(G.screen===SCR.HELP){ if(k==="Enter"||k==="Escape"||kl==="?") G.screen=SCR.MAP; return; }
    if(G.screen===SCR.WIN||G.screen===SCR.LOSE){ if(k==="Enter"){ newGame((Math.random()*1e9)|0); G.screen=SCR.MAP; } else if(k==="Escape"){ newGame(G.seedNum); G.screen=SCR.TITLE; } return; }

    if(G.screen===SCR.INF){
      const inf=G.inf, n=inf.n;
      if(inf.mode==="pw"){
        const list=pwList(n);
        if(k==="ArrowUp"||kl==="k") inf.pwSel=(inf.pwSel-1+list.length)%list.length;
        else if(k==="ArrowDown"||kl==="j") inf.pwSel=(inf.pwSel+1)%list.length;
        else if(k==="Enter"){ inf.mode="tools"; tryPassword(inf.pwSel); }
        else if(k==="Escape") inf.mode="tools";
        e.preventDefault(); return;
      }
      if(k==="Escape"){ G.trace=Math.min(100,G.trace+4); log("- aborted "+n.ip); G.inf=null; G.screen=SCR.MAP; }
      else if(k==="ArrowUp"||kl==="k"){ const m=toolIds().length; inf.tool=(inf.tool-1+m)%m; }
      else if(k==="ArrowDown"||kl==="j"){ const m=toolIds().length; inf.tool=(inf.tool+1)%m; }
      else if(k==="ArrowLeft"||kl==="h"){ const m=Math.max(1,n.ports.length); inf.vec=(inf.vec-1+m)%m; }
      else if(k==="ArrowRight"||kl==="l"){ const m=Math.max(1,n.ports.length); inf.vec=(inf.vec+1)%m; }
      else if(k==="Enter"||k===" "){ infiltrateAttempt(); }
      else if(kl==="g"&&n.hasLogin){ inf.mode="pw"; inf.pwSel=0; }
      e.preventDefault(); return;
    }

    // MAP
    if(k==="Escape"){ G.screen=SCR.TITLE; return; }
    if(kl==="?"){ G.screen=SCR.HELP; return; }
    const list=curList();
    if(k==="ArrowUp"||kl==="k"){ G.sel=Math.max(0,G.sel-1); }
    else if(k==="ArrowDown"||kl==="j"){ G.sel=Math.min(list.length-1,G.sel+1); }
    else if(kl==="r"){ toggleConnect(); }
    else if(kl==="s"){ const n=selNode(); if(n) scan(n); }
    else if(kl==="i"){ const n=selNode(); if(n) startInfiltrate(n); }
    else if(kl==="l"){ const n=selNode(); if(n) loot(n); }
    else if(k==="Enter"){ const n=selNode(); if(!n)return;
      if(n.state==="owned"){ if(n.loot>0 && !n.looted && n.kind!=="router") loot(n); else scan(n); }
      else if(n.state==="discovered") startInfiltrate(n); }
    if(["ArrowUp","ArrowDown","Enter"," "].includes(k)) e.preventDefault();
  }

  // ---- WebAudio (compact) --------------------------------------------------
  const Sound=(function(){ let ctx=null,last={}; const S={enabled:true};
    function ac(){ if(!S.enabled)return null; if(!ctx){try{ctx=new (window.AudioContext||window.webkitAudioContext)();}catch(e){return null;}} if(ctx.state==="suspended")ctx.resume(); return ctx; }
    function t(f,d,tm,ty,g){ const o=ctx.createOscillator(),gn=ctx.createGain();o.type=ty||"sine";o.frequency.value=f;o.connect(gn);gn.connect(ctx.destination);const a=g==null?.12:g;gn.gain.setValueAtTime(0,tm);gn.gain.linearRampToValueAtTime(a,tm+.008);gn.gain.setValueAtTime(a,tm+d-.02);gn.gain.linearRampToValueAtTime(0,tm+d);o.start(tm);o.stop(tm+d); }
    function nz(d,tm,g){ const n=Math.max(1,(ctx.sampleRate*d)|0),b=ctx.createBuffer(1,n,ctx.sampleRate),da=b.getChannelData(0);for(let i=0;i<n;i++)da[i]=Math.random()*2-1;const s=ctx.createBufferSource();s.buffer=b;const f=ctx.createBiquadFilter();f.type="bandpass";f.frequency.value=1800;f.Q.value=.6;const gn=ctx.createGain();gn.gain.value=g==null?.05:g;s.connect(f);f.connect(gn);gn.connect(ctx.destination);s.start(tm);s.stop(tm+d); }
    function rate(k,ms){const n=performance.now();if(n-(last[k]||0)<ms)return false;last[k]=n;return true;}
    return { enabled:true, resume(){ac();},
      dial(){ if(!ac()||!rate("d",90))return; const c=ctx.currentTime; t(941,.05,c,"sine",.06);t(1336,.05,c,"sine",.06); },
      busy(){ if(!ac()||!rate("b",260))return; let c=ctx.currentTime; for(let i=0;i<2;i++){t(480,.16,c,"sine",.08);t(620,.16,c,"sine",.08);c+=.27;} },
      levelup(){ if(!ac())return; let c=ctx.currentTime;[523,659,784,1047].forEach((f,i)=>t(f,.12,c+i*.1,"square",.07)); },
      gameover(){ if(!ac())return; let c=ctx.currentTime;[392,330,262,196].forEach((f,i)=>t(f,.26,c+i*.22,"sawtooth",.08)); nz(1,c+.9,.04); },
      connect(){ if(!ac())return; let c=ctx.currentTime; t(2100,.4,c,"sine",.1);c+=.4; for(let i=0;i<5;i++){t(1100+(i%2?700:-260),.1,c,"square",.045);nz(.1,c,.045);c+=.1;} },
    };
  })();

  // Optional test hook (only when window.__NETRUN_TEST is set before load).
  try { if (window.__NETRUN_TEST) window.__N = { get G(){return G;}, get scr(){return scr;}, N, toolIds }; } catch(e){}

  // ---- boot ----------------------------------------------------------------
  window.addEventListener("DOMContentLoaded",function(){
    scr=new Screen(document.getElementById("cv"));
    newGame((Math.random()*1e9)|0);
    document.addEventListener("keydown",onKey);
    document.body.addEventListener("click",()=>Sound.resume(),{once:true});
    requestAnimationFrame(tick);
  });
})();
