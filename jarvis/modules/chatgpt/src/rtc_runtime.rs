//! ICE sockets follow the same authenticated SOCKS route as signaling.
//! UDP is enabled only when the VPN explicitly advertises a UDP relay.
use std::{io,fmt,future::Future,pin::Pin,sync::{Arc,Mutex,atomic::{AtomicBool,Ordering}},net::{SocketAddr,TcpListener,UdpSocket},task::Waker,time::Duration};
use tokio::io::{AsyncReadExt,AsyncWriteExt};
use webrtc::runtime::{Runtime,TokioRuntime,AsyncTcpStream,AsyncTcpListener,AsyncUdpSocket,AsyncInterval,JoinHandle};
type IoFuture<'a,T>=Pin<Box<dyn Future<Output=io::Result<T>>+Send+'a>>;
pub struct RoutedRuntime{route:Option<Route>,armed:Arc<AtomicBool>,arm_wakers:Arc<Mutex<Vec<Waker>>>}
#[derive(Clone)]
pub(super) struct Route{pub address:SocketAddr,pub user:Vec<u8>,pub pass:Vec<u8>,pub udp:bool}
impl fmt::Debug for RoutedRuntime{fn fmt(&self,f:&mut fmt::Formatter<'_>)->fmt::Result{f.debug_struct("RoutedRuntime").field("proxied",&self.route.is_some()).field("armed",&self.armed.load(Ordering::Relaxed)).finish()}}
impl RoutedRuntime{
    #[cfg(test)]
    pub fn new(network:&serde_json::Value)->Result<Self,String>{Self::with_armed(network,true)}
    pub fn new_standby(network:&serde_json::Value)->Result<Self,String>{Self::with_armed(network,false)}
    fn with_armed(network:&serde_json::Value,armed:bool)->Result<Self,String>{
        if let Some(error)=network["error"].as_str(){return Err(error.into());}
        let route=if let Some(address)=network["socks"].as_str(){
            let address:SocketAddr=address.parse().map_err(|_|"Неверный SOCKS VPN")?;let user=network["user"].as_str().unwrap_or("").as_bytes().to_vec();let pass=network["pass"].as_str().unwrap_or("").as_bytes().to_vec();
            if !address.ip().is_loopback() || address.port()==0 || !(1..=255).contains(&user.len()) || !(1..=255).contains(&pass.len()){return Err("Неверный SOCKS VPN".into());}Some(Route{address,user,pass,udp:network["udp"].as_bool().unwrap_or(false)})
        }else{None};
        Ok(Self{route,armed:Arc::new(AtomicBool::new(armed)),arm_wakers:Arc::default()})
    }
    pub fn arm(&self){
        let mut wakers=self.arm_wakers.lock().unwrap();
        self.armed.store(true,Ordering::Release);
        for waker in wakers.drain(..){waker.wake();}
    }
    pub fn proxied(&self)->bool{self.route.is_some()}
    pub fn udp(&self)->bool{self.route.as_ref().is_none_or(|route|route.udp)}
}
impl Runtime for RoutedRuntime {
    fn spawn(&self,f:Pin<Box<dyn Future<Output=()>+Send>>)->Box<dyn JoinHandle>{TokioRuntime.spawn(f)}
    fn spawn_reactor(&self,n:usize,f:Pin<Box<dyn Future<Output=()>+Send>>)->Box<dyn JoinHandle>{TokioRuntime.spawn_reactor(n,f)}
    fn wrap_udp_socket(&self,s:UdpSocket)->io::Result<Arc<dyn AsyncUdpSocket>>{match &self.route{Some(route) if route.udp=>Ok(Arc::new(super::rtc_udp::Socket::new(s,route.clone(),self.armed.clone(),self.arm_wakers.clone())?)),Some(_)=>Err(io::Error::new(io::ErrorKind::PermissionDenied,"VPN has no UDP relay")),None=>TokioRuntime.wrap_udp_socket(s)}}
    fn wrap_tcp_listener(&self,l:TcpListener)->io::Result<Arc<dyn AsyncTcpListener>>{if self.proxied(){Ok(Arc::new(NoAccept(l)))}else{TokioRuntime.wrap_tcp_listener(l)}}
    fn connect_tcp<'a>(&'a self,remote:SocketAddr)->IoFuture<'a,Arc<dyn AsyncTcpStream>>{
        Box::pin(async move{
            let Some(route)=&self.route else{return TokioRuntime.connect_tcp(remote).await;};
            tokio::time::timeout(Duration::from_secs(10),async{
                let mut socket=tokio::net::TcpStream::connect(route.address).await?;socket.set_nodelay(true)?;socket.write_all(&[5,1,2]).await?;
                let mut response=[0;2];socket.read_exact(&mut response).await?;if response!=[5,2]{return Err(io::Error::other("SOCKS authentication required"));}
                let mut auth=vec![1,route.user.len() as u8];auth.extend(&route.user);auth.push(route.pass.len() as u8);auth.extend(&route.pass);socket.write_all(&auth).await?;socket.read_exact(&mut response).await?;if response!=[1,0]{return Err(io::Error::other("SOCKS authentication rejected"));}
                let mut request=vec![5,1,0];match remote.ip(){std::net::IpAddr::V4(ip)=>{request.push(1);request.extend(ip.octets());},std::net::IpAddr::V6(ip)=>{request.push(4);request.extend(ip.octets());}}request.extend(remote.port().to_be_bytes());socket.write_all(&request).await?;
                let mut header=[0;4];socket.read_exact(&mut header).await?;if header[..3]!=[5,0,0]{return Err(io::Error::other("SOCKS connect rejected"));}
                let len=match header[3]{1=>4,4=>16,3=>socket.read_u8().await? as usize,_=>return Err(io::Error::other("SOCKS address type"))};let mut rest=vec![0;len+2];socket.read_exact(&mut rest).await?;
                let local=socket.local_addr()?;let (read,write)=socket.into_split();Ok(Arc::new(Stream{read:tokio::sync::Mutex::new(read),write:tokio::sync::Mutex::new(write),local,remote}) as Arc<dyn AsyncTcpStream>)
            }).await.map_err(|_|io::Error::new(io::ErrorKind::TimedOut,"SOCKS ICE connect timeout"))?
        })
    }
    fn resolve_host<'a>(&'a self,host:&'a str)->IoFuture<'a,Vec<SocketAddr>>{if self.proxied(){Box::pin(async move{host.parse().map(|address|vec![address]).map_err(|_|io::Error::new(io::ErrorKind::PermissionDenied,"direct DNS disabled for VPN ICE"))})}else{TokioRuntime.resolve_host(host)}}
    fn sleep(&self,d:Duration)->Pin<Box<dyn Future<Output=()>+Send+'static>>{TokioRuntime.sleep(d)}
    fn interval(&self,d:Duration)->Box<dyn AsyncInterval>{TokioRuntime.interval(d)}
    fn block_on(&self,f:Pin<Box<dyn Future<Output=()>+'_>>){TokioRuntime.block_on(f)}
}
#[derive(Debug)]struct NoAccept(TcpListener);
impl AsyncTcpListener for NoAccept{fn accept<'a>(&'a self)->IoFuture<'a,(Arc<dyn AsyncTcpStream>,SocketAddr)>{Box::pin(std::future::pending())}fn local_addr(&self)->io::Result<SocketAddr>{self.0.local_addr()}}
#[derive(Debug)]struct Stream{read:tokio::sync::Mutex<tokio::net::tcp::OwnedReadHalf>,write:tokio::sync::Mutex<tokio::net::tcp::OwnedWriteHalf>,local:SocketAddr,remote:SocketAddr}
impl AsyncTcpStream for Stream{
    fn read<'a,'b>(&'a self,buf:&'b mut [u8])->IoFuture<'b,usize> where 'a:'b{Box::pin(async move{self.read.lock().await.read(buf).await})}
    fn write_all<'a,'b>(&'a self,buf:&'b [u8])->IoFuture<'b,()> where 'a:'b{Box::pin(async move{self.write.lock().await.write_all(buf).await})}
    fn local_addr(&self)->io::Result<SocketAddr>{Ok(self.local)}fn peer_addr(&self)->io::Result<SocketAddr>{Ok(self.remote)}
}
#[cfg(test)]mod tests{
    use super::*;use serde_json::json;
    #[tokio::test]
    async fn vpn_ice_uses_authenticated_socks_and_blocks_direct_udp_and_dns(){
        let listener=tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();let route=listener.local_addr().unwrap();
        let runtime=RoutedRuntime::new(&json!({"socks":route.to_string(),"user":"user","pass":"pass"})).unwrap();
        assert!(runtime.wrap_udp_socket(UdpSocket::bind("127.0.0.1:0").unwrap()).is_err());
        assert!(runtime.resolve_host("forbidden.invalid:443").await.is_err());
        let server=tokio::spawn(async move{let (mut socket,_)=listener.accept().await.unwrap();let mut greeting=[0;3];socket.read_exact(&mut greeting).await.unwrap();assert_eq!(greeting,[5,1,2]);socket.write_all(&[5,2]).await.unwrap();let mut auth=[0;11];socket.read_exact(&mut auth).await.unwrap();assert_eq!(&auth,&[1,4,b'u',b's',b'e',b'r',4,b'p',b'a',b's',b's']);socket.write_all(&[1,0]).await.unwrap();let mut connect=[0;10];socket.read_exact(&mut connect).await.unwrap();assert_eq!(connect,[5,1,0,1,127,0,0,1,34,184]);socket.write_all(&[5,0,0,1,127,0,0,1,0,1]).await.unwrap();let mut data=[0;5];socket.read_exact(&mut data).await.unwrap();assert_eq!(&data,b"hello");socket.write_all(b"ok").await.unwrap();});
        let stream=runtime.connect_tcp("127.0.0.1:8888".parse().unwrap()).await.unwrap();stream.write_all(b"hello").await.unwrap();let mut response=[0;2];stream.read(&mut response).await.unwrap();assert_eq!(&response,b"ok");server.await.unwrap();
    }
}
